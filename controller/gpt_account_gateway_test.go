package controller

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/sub2api"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGPTAccountGatewayConfigurationProtectsKeysAndChannel(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	input := service.GPTAccountGatewayInput{Name: "Pool", Provider: "cpa", ManagementVersion: "v8", ManagementURL: "https://cpa.example", RelayURL: "https://cpa.example", ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"}
	gateway, err := service.SaveGPTAccountGateway(nil, input)
	require.NoError(t, err)
	require.NotNil(t, gateway.ChannelID)
	assert.Equal(t, int64(1), gateway.Generation)
	encoded, err := common.Marshal(gateway)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "management-secret")
	assert.NotContains(t, string(encoded), "relay-secret")
	assert.NotContains(t, gateway.SecretsCiphertext, "secret")
	secrets, err := service.DecryptGPTAccountGatewaySecrets(gateway)
	require.NoError(t, err)
	assert.Equal(t, "management-secret", secrets.ManagementKey)
	tampered := *gateway
	tampered.SecretID = "another-gateway"
	_, err = service.DecryptGPTAccountGatewaySecrets(&tampered)
	assert.ErrorIs(t, err, service.ErrGPTAccountEncryption)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, *gateway.ChannelID).Error)
	assert.Equal(t, 1, channel.Type)
	assert.Equal(t, "relay-secret", channel.Key)
	original := *gateway
	input.Name, input.ManagementKey, input.RelayKey, input.Group = "Renamed pool", "", "rotated-relay", "other"
	gateway, err = service.SaveGPTAccountGateway(gateway, input)
	require.NoError(t, err)
	assert.Equal(t, original.ChannelID, gateway.ChannelID)
	require.NoError(t, model.DB.First(&channel, *gateway.ChannelID).Error)
	assert.Equal(t, "rotated-relay", channel.Key)
	assert.Equal(t, "other", channel.Group)
	_, err = service.SaveGPTAccountGateway(&original, input)
	assert.ErrorIs(t, err, model.ErrGPTAccountStale)
	input.ManagementURL = "https://another.example"
	_, err = service.SaveGPTAccountGateway(gateway, input)
	assert.EqualError(t, err, "gateway_immutable")
}

func TestGPTAccountGatewayRejectsUnsafeConfiguration(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	for _, endpoint := range []string{"http://public.example", "http://127.0.0.1:8080", "https://u:p@public.example", "https://public.example/?key=secret", "https://public.example/#secret", "https://public.example/a/../admin", "https://public.example/%2e%2e/admin", "https://169.254.169.254", "https://10.0.0.1"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: "cpa", ManagementVersion: "v8", ManagementURL: endpoint, RelayURL: "https://public.example", ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
			assert.EqualError(t, err, "invalid_gateway")
		})
	}
}

func TestParseGPTGatewayChannelJSONAcceptsDirectCPAAndSub2APIConfigs(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		provider string
		baseURL  string
		key      string
		models   string
		group    string
	}{
		{
			name:     "CPA aliases",
			raw:      `{"provider":"cpa","name":"CPA relay","base_url":"https://cpa.example/v1","api_key":"cpa-key","models":["gpt-5","gpt-5-mini"],"group":"premium"}`,
			provider: "cpa",
			baseURL:  "https://cpa.example",
			key:      "cpa-key",
			models:   "gpt-5,gpt-5-mini",
			group:    "premium",
		},
		{
			name:     "CPA path prefix and model list",
			raw:      `{"provider":"cpa","base_url":"https://cpa.example/relay/v1/","api_key":"cpa-key","models":"gpt-5, gpt-5-mini"}`,
			provider: "cpa",
			baseURL:  "https://cpa.example/relay",
			key:      "cpa-key",
			models:   "gpt-5,gpt-5-mini",
			group:    "",
		},
		{
			name:     "Sub2API base URL",
			raw:      `{"type":"sub2api","baseUrl":"https://sub2api.example/v1/","apiKey":"sub-key","model":"gpt-5"}`,
			provider: "sub2api",
			baseURL:  "https://sub2api.example",
			key:      "sub-key",
			models:   "gpt-5",
			group:    "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.ParseGPTGatewayChannelJSON(test.raw)
			require.NoError(t, err)
			assert.Equal(t, test.provider, got.Provider)
			assert.Equal(t, test.baseURL, got.BaseURL)
			assert.Equal(t, test.key, got.APIKey)
			assert.Equal(t, test.models, got.Models)
			assert.Equal(t, test.group, got.Group)
			for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/responses/compact"} {
				info := &relaycommon.RelayInfo{
					ChannelMeta:    &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, ChannelBaseUrl: got.BaseURL},
					RequestURLPath: path,
					RelayMode:      relayconstant.Path2RelayMode(path),
				}
				var requestURL string
				if got.Provider == "sub2api" {
					info.ChannelType = constant.ChannelTypeSub2API
					requestURL, err = (&sub2api.Adaptor{}).GetRequestURL(info)
				} else {
					requestURL, err = (&openai.Adaptor{}).GetRequestURL(info)
				}
				require.NoError(t, err)
				assert.Equal(t, test.baseURL+path, requestURL)
			}
		})
	}
}

func TestParseGPTGatewayChannelJSONRejectsUnroutableModels(t *testing.T) {
	for _, models := range []any{"", ",", "gpt-5,,gpt-5-mini", "gpt-5,", strings.Repeat("m", 256), []string{"gpt-5", strings.Repeat("m", 256)}} {
		raw, err := common.Marshal(map[string]any{"provider": "cpa", "base_url": "https://cpa.example", "api_key": "fixture-key", "models": models})
		require.NoError(t, err)
		_, err = service.ParseGPTGatewayChannelJSON(string(raw))
		assert.EqualError(t, err, "gateway_channel_models_invalid")
	}
}

func TestParseGPTGatewayChannelJSONRejectsAccountCredentialDocuments(t *testing.T) {
	for _, raw := range []string{
		`{"type":"oauth","credentials":{"access_token":"private"}}`,
		`{"provider":"sub2api","base_url":"https://sub2api.example","credentials":{"access_token":"private"}}`,
		`{"provider":"cpa","base_url":"https://cpa.example"}`,
		`{"provider":"other","base_url":"https://example.com","api_key":"key"}`,
		`{"provider":"cpa","base_url":"http://relay.example","api_key":"key"}`,
		`{"provider":"cpa","base_url":"https://127.0.0.1:8080","api_key":"key"}`,
		`{"provider":"cpa","base_url":"https://relay.example/?token=secret","api_key":"key"}`,
	} {
		_, err := service.ParseGPTGatewayChannelJSON(raw)
		assert.Error(t, err)
	}
}

func TestCreateGPTGatewayChannelCreatesOneRelayChannelAtomically(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	input, err := service.ParseGPTGatewayChannelJSON(`{"provider":"sub2api","base_url":"https://sub2api.example/v1","api_key":"relay-key","models":["gpt-5"]}`)
	require.NoError(t, err)
	channel, err := service.CreateGPTGatewayChannel(input)
	require.NoError(t, err)
	require.NotZero(t, channel.Id)
	assert.Equal(t, 59, channel.Type)
	assert.Equal(t, "https://sub2api.example", channel.GetBaseURL())
	assert.Equal(t, "relay-key", channel.Key)
	var abilities int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Count(&abilities).Error)
	assert.Positive(t, abilities)
}

func setupGPTAccountGatewayTest(t *testing.T) service.AuthIdentity {
	t.Helper()
	identity := setupGPTAccountTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountGateway{}))
	require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountGateway{}, &model.GPTAccount{}))
	return identity
}

func gatewayAccountFixture(t *testing.T, identity service.AuthIdentity, gateway *model.GPTAccountGateway, suffix string) (*model.GPTAccount, service.CodexOAuthKey) {
	t.Helper()
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed " + suffix, Email: suffix + "@example.com", GatewayID: &gateway.ID, SecretID: "owned-" + suffix, Generation: 1, Status: "pending"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
	require.NoError(t, model.DB.Create(&account).Error)
	snapshot, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	snapshot.OAuthSubject = "subject-" + suffix
	key := service.CodexOAuthKey{Type: "codex", Email: account.Email, AccountID: "remote-" + suffix, AccessToken: "private-access", RefreshToken: "private-refresh", IDToken: "private-id-token", LastRefresh: time.Now().Format(time.RFC3339), Expired: time.Now().Add(time.Hour).Format(time.RFC3339)}
	return snapshot, key
}

func gatewayFixture(t *testing.T, provider, endpoint, version string) *model.GPTAccountGateway {
	t.Helper()
	gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: provider, ManagementVersion: version, ManagementURL: endpoint, RelayURL: endpoint, AllowLocalHTTP: true, RemoteGroupID: 17, ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	return gateway
}

func TestGPTAccountGatewayCPAImportOwnsOnePoolAndReauthorizes(t *testing.T) {
	for _, version := range []string{"v8", "legacy"} {
		t.Run(version, func(t *testing.T) {
			identity := setupGPTAccountGatewayTest(t)
			records := map[string]map[string]any{}
			posts := 0
			prefix := "/v8/management/credentials"
			if version == "legacy" {
				prefix = "/v0/management/auth-files"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer management-secret", r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("x-api-key"))
				assert.True(t, strings.HasPrefix(r.URL.Path, prefix))
				name := r.URL.Query().Get("name")
				if r.Method == http.MethodGet {
					row, ok := records[name]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					body, err := common.Marshal(row)
					require.NoError(t, err)
					_, _ = w.Write(body)
					return
				}
				require.Equal(t, prefix, r.URL.Path)
				posts++
				var row map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &row))
				assert.Equal(t, "codex", row["type"])
				assert.NotEmpty(t, row["new_api_account"])
				assert.NotEmpty(t, row["new_api_import"])
				records[name] = row
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}))
			defer upstream.Close()
			gateway := gatewayFixture(t, "cpa", upstream.URL, version)
			first, key := gatewayAccountFixture(t, identity, gateway, "first")
			require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), first, key))
			service.ReleaseGPTAccountJob(first, "")
			second, secondKey := gatewayAccountFixture(t, identity, gateway, "second")
			require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), second, secondKey))
			service.ReleaseGPTAccountJob(second, "")
			var accounts []model.GPTAccount
			require.NoError(t, model.DB.Order("id").Find(&accounts).Error)
			require.NoError(t, service.PopulateGPTAccountGateways(accounts))
			require.Len(t, accounts, 2)
			for _, account := range accounts {
				assert.Nil(t, account.ChannelID)
				assert.Equal(t, gateway.ChannelID, account.GatewayChannelID)
				assert.Equal(t, "synced", account.GatewaySyncStatus)
				secrets, err := service.DecryptGPTAccountSecrets(&account)
				require.NoError(t, err)
				assert.Nil(t, secrets.GatewayOAuth)
				assert.NotEmpty(t, secrets.GatewayRemoteID)
				assert.Equal(t, "private-password", secrets.Password)
				assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
			}
			assert.Len(t, records, 2)
			var channels int64
			require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channels).Error)
			assert.Equal(t, int64(1), channels)
			assert.False(t, service.ScheduleGPTAccountRecovery(*gateway.ChannelID, "relay-secret"))
			require.NoError(t, service.SyncGPTAccountGateway(context.Background(), first.ID))
			assert.Equal(t, 2, posts, "sync must not reimport an obsolete refresh token")
			relogin, err := service.AcquireGPTAccountJob(first.ID, false)
			require.NoError(t, err)
			key.AccessToken = "replacement-access"
			require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), relogin, key))
			service.ReleaseGPTAccountJob(relogin, "")
			assert.Equal(t, 3, posts)
			assert.Len(t, records, 2)
			assert.ErrorIs(t, model.StoreGPTAccountCredential(relogin, "not-direct", key.AccountID), model.ErrGPTAccountStale)
		})
	}
}

func TestGPTAccountGatewaySub2APIRecoversUncertainCreateAndUpdatesOnlyOwnedRecord(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	var remote map[string]any
	creates, updates := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "management-secret", r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		if r.Method == http.MethodGet {
			data := any(remote)
			if r.URL.Path == "/api/v1/admin/accounts" {
				items := []map[string]any{}
				if remote != nil {
					items = append(items, remote)
				}
				data = map[string]any{"items": items, "total": len(items)}
			}
			body, err := common.Marshal(map[string]any{"code": 0, "data": data})
			require.NoError(t, err)
			_, _ = w.Write(body)
			return
		}
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		credentials := body["credentials"].(map[string]any)
		assert.Equal(t, "remote-first", credentials["chatgpt_account_id"])
		assert.NotContains(t, credentials, "account_id")
		assert.NotEmpty(t, credentials["expires_at"])
		assert.NotEmpty(t, credentials["client_id"])
		if r.URL.Path == "/api/v1/admin/accounts" {
			creates++
			assert.Equal(t, []any{float64(17)}, body["group_ids"])
			remote = body
			remote["id"] = 42
			// The remote write succeeded but its response is unavailable.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("private-response private-refresh"))
			return
		}
		assert.Equal(t, "/api/v1/admin/accounts/42/apply-oauth-credentials", r.URL.Path)
		updates++
		assert.NotContains(t, body, "group_ids")
		assert.NotContains(t, body, "concurrency")
		assert.NotContains(t, body, "name")
		remote["credentials"], remote["extra"] = body["credentials"], body["extra"]
		encoded, err := common.Marshal(map[string]any{"code": 0, "data": remote})
		require.NoError(t, err)
		_, _ = w.Write(encoded)
	}))
	defer upstream.Close()
	gateway := gatewayFixture(t, "sub2api", upstream.URL, "")
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
	err := service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
	require.EqualError(t, err, "gateway_import_uncertain")
	assert.NotContains(t, err.Error(), "private")
	service.ReleaseGPTAccountJob(snapshot, err.Error())
	// A completed remote import can still be recovered after the locally retained
	// access token expires; do not replay that stale token to the remote gateway.
	var pending model.GPTAccount
	require.NoError(t, model.DB.First(&pending, snapshot.ID).Error)
	pendingSecrets, err := service.DecryptGPTAccountSecrets(&pending)
	require.NoError(t, err)
	pendingSecrets.GatewayOAuth.Expired = time.Now().Add(-time.Minute).Format(time.RFC3339)
	require.NoError(t, service.EncryptGPTAccountSecrets(&pending, *pendingSecrets))
	require.NoError(t, model.DB.Model(&pending).Update("secrets_ciphertext", pending.SecretsCiphertext).Error)
	require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
	assert.Equal(t, 1, creates)
	assert.Zero(t, updates, "matching completed attempt must not replay its obsolete refresh token")
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "42", secrets.GatewayRemoteID)
	assert.Nil(t, secrets.GatewayOAuth)
	assert.Equal(t, "ready", account.Status)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, *gateway.ChannelID).Error)
	assert.Equal(t, 59, channel.Type)
	snapshot, err = service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	key.AccessToken = "new-verified-token"
	require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
	service.ReleaseGPTAccountJob(snapshot, "")
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, updates)
	remote["extra"] = map[string]any{"new_api_account": "another-owner"}
	err = service.SyncGPTAccountGateway(context.Background(), account.ID)
	assert.EqualError(t, err, "gateway_remote_conflict")
	assert.Equal(t, 1, updates, "never overwrite an unrelated remote account")
}

func TestGPTAccountGatewaySub2APIUnknownCreateIsNeverBlindlyRepeated(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	creates := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0}}`))
			return
		}
		creates++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	gateway := gatewayFixture(t, "sub2api", upstream.URL, "")
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
	err := service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
	require.EqualError(t, err, "gateway_import_uncertain")
	service.ReleaseGPTAccountJob(snapshot, err.Error())
	assert.EqualError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID), "gateway_import_uncertain")
	assert.Equal(t, 1, creates)
}

func TestGPTAccountGatewayRejectedImportsCanRetry(t *testing.T) {
	for _, provider := range []string{"cpa", "sub2api"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				identity := setupGPTAccountGatewayTest(t)
				var remote map[string]any
				posts := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						posts++
						if posts == 1 {
							w.WriteHeader(status)
							_, _ = w.Write([]byte(`{"message":"private rejection details"}`))
							return
						}
						require.NoError(t, common.DecodeJson(r.Body, &remote))
						if provider == "cpa" {
							_, _ = w.Write([]byte(`{"status":"ok"}`))
							return
						}
						remote["id"] = 42
					}
					var data any = remote
					if provider == "cpa" && remote == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if provider == "sub2api" {
						if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts" {
							items := []map[string]any{}
							if remote != nil {
								items = append(items, remote)
							}
							data = map[string]any{"items": items, "total": len(items)}
						}
						data = map[string]any{"code": 0, "data": data}
					}
					body, err := common.Marshal(data)
					require.NoError(t, err)
					_, _ = w.Write(body)
				}))
				defer upstream.Close()
				version := ""
				if provider == "cpa" {
					version = "v8"
				}
				gateway := gatewayFixture(t, provider, upstream.URL, version)
				snapshot, key := gatewayAccountFixture(t, identity, gateway, "rejected")
				err := service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
				require.EqualError(t, err, "gateway_import_failed")
				service.ReleaseGPTAccountJob(snapshot, err.Error())
				if status == http.StatusUnauthorized {
					// A fresh verified login must also recover from a rejected import.
					snapshot, err = service.AcquireGPTAccountJob(snapshot.ID, false)
					require.NoError(t, err)
					err = service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
					service.ReleaseGPTAccountJob(snapshot, service.GPTAccountErrorCode(err))
					require.NoError(t, err)
				} else {
					require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
				}
				assert.Equal(t, 2, posts)
				require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
				assert.Equal(t, 2, posts, "confirmed imports must not be submitted again")
			})
		}
	}
}

func TestGPTAccountGatewayRejectsRedirectAndOversizeWithoutLeakingCredentials(t *testing.T) {
	for _, mode := range []string{"redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			identity := setupGPTAccountGatewayTest(t)
			leaked := 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked++ }))
			defer destination.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write([]byte(strings.Repeat("x", (2<<20)+1)))
			}))
			defer upstream.Close()
			gateway := gatewayFixture(t, "cpa", upstream.URL, "v8")
			snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
			err := service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
			assert.EqualError(t, err, "gateway_import_failed")
			assert.Zero(t, leaked)
		})
	}
}

func TestGPTAccountGatewayConfigChangeFencesLateRemoteImport(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	var gateway *model.GPTAccountGateway
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writes++
		_, err := service.SaveGPTAccountGateway(gateway, service.GPTAccountGatewayInput{Name: "Edited pool", Provider: "cpa", ManagementVersion: "v8", ManagementURL: gateway.ManagementURL, RelayURL: gateway.RelayURL, AllowLocalHTTP: true, RemoteGroupID: 17, Group: "edited", Models: "gpt-5"})
		require.NoError(t, err)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()
	gateway = gatewayFixture(t, "cpa", upstream.URL, "v8")
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
	require.Error(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
	assert.Equal(t, 1, writes)
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	assert.Equal(t, int64(2), account.Generation)
	assert.Equal(t, "pending", account.GatewaySyncStatus)
	assert.NotEqual(t, "ready", account.Status)
	assert.Zero(t, account.GatewaySyncedAt)
}

func gatewayRequest(t *testing.T, identity service.AuthIdentity, role, id int, method, body, proof string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	return gptAccountRequest(t, identity, role, id, method, body, "", func(c *gin.Context) { c.Request.Header.Set("X-Security-Proof", proof); handler(c) })
}

func TestGPTAccountGatewayRootManagementProofAndSafeTargetList(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	body := `{"name":"Pool","provider":"cpa","management_version":"v8","management_url":"https://cpa.example","relay_url":"https://relay.example","management_key":"management-secret","relay_key":"relay-secret","group":"default","models":"gpt-5"}`
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "gpt_account_gateway.create", Context: []byte(`{"name":"Pool"}`)}, service.VerificationMethodPassword)
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		denied := gatewayRequest(t, identity, role, 0, http.MethodPost, body, proof, CreateGPTAccountGateway)
		assert.Equal(t, http.StatusForbidden, denied.Code)
	}
	denied := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, "", CreateGPTAccountGateway)
	assert.Equal(t, http.StatusForbidden, denied.Code)
	created := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, proof, CreateGPTAccountGateway)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.NotContains(t, created.Body.String(), "management-secret")
	assert.NotContains(t, created.Body.String(), "relay-secret")
	replay := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, proof, CreateGPTAccountGateway)
	assert.Equal(t, http.StatusForbidden, replay.Code)
	var gateway model.GPTAccountGateway
	require.NoError(t, model.DB.First(&gateway).Error)
	for _, role := range []int{common.RoleAdminUser, common.RoleRootUser} {
		listed := gatewayRequest(t, identity, role, 0, http.MethodGet, "", "", ListGPTAccountGateways)
		require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
		assert.Contains(t, listed.Body.String(), `"items":[`)
		assert.Contains(t, listed.Header().Get("Cache-Control"), "no-store")
		assert.NotContains(t, listed.Body.String(), "management-secret")
		assert.NotContains(t, listed.Body.String(), "relay-secret")
		if role == common.RoleAdminUser {
			assert.NotContains(t, listed.Body.String(), "management_url")
			assert.NotContains(t, listed.Body.String(), "relay_url")
		}
	}
	ordinary := gatewayRequest(t, identity, common.RoleCommonUser, 0, http.MethodGet, "", "", ListGPTAccountGateways)
	assert.Equal(t, http.StatusForbidden, ordinary.Code)
	createdAccount := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, fmt.Sprintf(`{"name":"Pooled","email":"owner@example.com","gateway_id":%d}`, gateway.ID), "gpt_account.create", CreateGPTAccount)
	require.Equal(t, http.StatusOK, createdAccount.Code, createdAccount.Body.String())
	assert.Contains(t, createdAccount.Body.String(), fmt.Sprintf(`"gateway_id":%d`, gateway.ID))
	assert.Contains(t, createdAccount.Body.String(), fmt.Sprintf(`"gateway_channel_id":%d`, *gateway.ChannelID))
	assert.Contains(t, createdAccount.Body.String(), `"channel_id":null`)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", "", ListGPTAccounts)
	assert.Contains(t, listed.Body.String(), `"gateway_provider":"cpa"`)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	raw, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "management-secret")
	assert.NotContains(t, string(raw), "relay-secret")
}

func TestGPTGatewayChannelJSONImportRequiresRootProofAndCreatesChannel(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	body := `{"config_json":"{\"provider\":\"sub2api\",\"name\":\"测试渠道\",\"base_url\":\"https://sub2api.example/v1\",\"api_key\":\"relay-secret\",\"models\":[\"gpt-5\"]}"}`
	gatewayProof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: "gpt_account_gateway.create", Context: []byte(`{"name":"channel-json"}`)}, service.VerificationMethodPassword)
	wrongScope := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, gatewayProof, ImportGPTGatewayChannelJSON)
	assert.Equal(t, http.StatusForbidden, wrongScope.Code)
	assert.Contains(t, wrongScope.Body.String(), "SECURITY_PROOF_SCOPE_MISMATCH")
	operation := service.VerificationOperation{Scope: "gpt_account_gateway.channel_import", Context: []byte(`{"config_sha256":"3477af6d92429ec7f8da7a6c5667abd9571305a70bdfed6227e62fa012a689a0"}`)}
	proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	denied := gatewayRequest(t, identity, common.RoleAdminUser, 0, http.MethodPost, body, proof, ImportGPTGatewayChannelJSON)
	assert.Equal(t, http.StatusForbidden, denied.Code)
	for _, change := range [][2]string{{"sub2api.example", "replacement.example"}, {"relay-secret", "replacement-key"}, {"gpt-5", "gpt-5-mini"}, {"测试渠道", "另一个渠道"}} {
		changed := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, strings.Replace(body, change[0], change[1], 1), proof, ImportGPTGatewayChannelJSON)
		assert.Equal(t, http.StatusForbidden, changed.Code)
		assert.Contains(t, changed.Body.String(), "SECURITY_PROOF_CONTEXT_MISMATCH")
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&count).Error)
	assert.Zero(t, count, "a rejected proof must not create a channel")
	created := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, proof, ImportGPTGatewayChannelJSON)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"provider":"sub2api"`)
	assert.NotContains(t, created.Body.String(), "relay-secret")
	replay := gatewayRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, proof, ImportGPTGatewayChannelJSON)
	assert.Equal(t, http.StatusForbidden, replay.Code)
	assert.Contains(t, replay.Body.String(), "SECURITY_PROOF_CONSUMED")
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel).Error)
	assert.Equal(t, constant.ChannelTypeSub2API, channel.Type)
	assert.Equal(t, "https://sub2api.example", channel.GetBaseURL())
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestGPTGatewayChannelJSONImportProofRequiresExactDigestContext(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	_, err := service.GetVerificationRequirements(identity, "gpt_account_gateway.channel_import")
	require.NoError(t, err)
	for _, context := range []string{`{}`, `{"name":"channel-json"}`, `{"config_sha256":null}`, `{"config_sha256":123}`, `{"config_sha256":"short"}`, `{"config_sha256":"` + strings.Repeat("z", 64) + `"}`, `{"config_sha256":"` + strings.Repeat("a", 64) + `","extra":true}`} {
		_, err := service.BindVerificationOperation(service.VerificationOperation{Scope: "gpt_account_gateway.channel_import", Context: []byte(context)})
		assert.ErrorIs(t, err, service.ErrVerificationContextInvalid)
	}
}

func TestGPTAccountGatewayImmutableAssociationAndRevokedOwner(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNotFound) }))
	defer upstream.Close()
	gateway := gatewayFixture(t, "cpa", upstream.URL, "v8")
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
	direct := 0
	assert.EqualError(t, service.ApplyGPTAccountGateway(snapshot, &direct), "gateway_immutable")
	directAccount := &model.GPTAccount{ID: 999}
	assert.EqualError(t, service.ApplyGPTAccountGateway(directAccount, &gateway.ID), "gateway_immutable")
	channelID := 123
	assert.EqualError(t, service.ApplyGPTAccountGateway(&model.GPTAccount{ChannelID: &channelID}, &gateway.ID), "gateway_immutable")
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("status", common.UserStatusDisabled).Error)
	assert.EqualError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key), "authorization_forbidden")
	assert.Zero(t, calls)
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	assert.Empty(t, account.GatewaySyncStatus)
}

// This exact persisted schema is gptAccountBeforeTOTP plus the three columns
// released in f3ed15087. Gateway fields are introduced by the migration below.
type gptAccountBeforeGateway struct {
	Released      gptAccountBeforeTOTP `gorm:"embedded"`
	TOTPVersion   int64
	TOTPUpdatedAt int64
	TOTPStatus    string `gorm:"type:varchar(32)"`
}

func (gptAccountBeforeGateway) TableName() string { return "gpt_accounts" }

func TestGPTAccountGatewaySchemaFreshAndReleasedUpgrade(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("f3ed15087_upgrade_%v", upgrade), func(t *testing.T) {
			db, _ := newAuditTestDatabase(t, dialect, os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN"))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			channel := model.Channel{Type: 57, Name: "Existing direct channel", Key: "original-oauth", Group: "default", Models: "gpt-5", Status: common.ChannelStatusEnabled}
			original := model.GPTAccount{OwnerID: 1, Name: "Existing", Email: "existing@example.com", SecretID: "old-account", Generation: 3, TOTPVersion: 2, TOTPStatus: "operator_confirmed", TOTPUpdatedAt: 1700000000}
			require.NoError(t, service.EncryptGPTAccountSecrets(&original, service.GPTAccountSecrets{Password: "original-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
			if upgrade {
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &gptAccountBeforeGateway{}))
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, channel.AddAbilities(db))
				original.ChannelID, original.ChannelKeyHash = &channel.Id, model.GPTChannelKeyHash(channel.Key)
				require.NoError(t, db.Omit("gateway_id", "gateway_sync_status", "gateway_error_code", "gateway_synced_at").Create(&original).Error)
				require.NoError(t, db.First(&original, original.ID).Error)
			}
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.GPTAccount{}, &model.GPTAccountGateway{}))
			}
			if upgrade {
				var migrated model.GPTAccount
				require.NoError(t, db.First(&migrated, original.ID).Error)
				assert.Equal(t, original, migrated)
				secrets, err := service.DecryptGPTAccountSecrets(&migrated)
				require.NoError(t, err)
				assert.Equal(t, "original-password", secrets.Password)
				assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
				var storedChannel model.Channel
				require.NoError(t, db.First(&storedChannel, channel.Id).Error)
				assert.Equal(t, channel.Key, storedChannel.Key)
				var abilities []model.Ability
				require.NoError(t, db.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
				require.Len(t, abilities, 1)
				assert.Equal(t, "gpt-5", abilities[0].Model)
				duplicate := model.GPTAccount{OwnerID: 1, Name: "Duplicate direct", SecretID: "duplicate", ChannelID: original.ChannelID, Generation: 1}
				assert.Error(t, db.Create(&duplicate).Error, "one direct account per channel remains enforced")
			}
			pooledID := 77
			for _, secretID := range []string{"gateway-member-a", "gateway-member-b"} {
				require.NoError(t, db.Create(&model.GPTAccount{OwnerID: 1, Name: "Pooled", SecretID: secretID, GatewayID: &pooledID, Generation: 1}).Error)
			}
			gatewayChannelID := 123
			first := model.GPTAccountGateway{Name: "Gateway one", SecretID: "unique-gateway", Provider: "cpa", ChannelID: &gatewayChannelID, Generation: 1}
			require.NoError(t, db.Create(&first).Error)
			assert.Error(t, db.Create(&model.GPTAccountGateway{Name: "Duplicate gateway channel", SecretID: "other-gateway", Provider: "cpa", ChannelID: &gatewayChannelID, Generation: 1}).Error)
			assert.Error(t, db.Create(&model.GPTAccountGateway{Name: "Duplicate gateway secret", SecretID: "unique-gateway", Provider: "cpa", Generation: 1}).Error)
		})
	}
}

func TestGPTAccountGatewayRemoteRefreshRejectsMalformedSuccess(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	refreshes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":42,"name":"owned","platform":"openai","type":"oauth","extra":{"new_api_account":"owned-first"},"credentials":{"email":"first@example.com","chatgpt_account_id":"remote-first"}}}`))
			return
		}
		assert.Equal(t, "/api/v1/admin/openai/accounts/42/refresh", r.URL.Path)
		refreshes++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	gateway := gatewayFixture(t, "sub2api", upstream.URL, "")
	snapshot, _ := gatewayAccountFixture(t, identity, gateway, "first")
	snapshot.AccountID = "remote-first"
	secrets, err := service.DecryptGPTAccountSecrets(snapshot)
	require.NoError(t, err)
	secrets.GatewayRemoteID = "42"
	require.NoError(t, service.EncryptGPTAccountSecrets(snapshot, *secrets))
	require.NoError(t, model.DB.Model(snapshot).Updates(map[string]any{"secrets_ciphertext": snapshot.SecretsCiphertext, "account_id": snapshot.AccountID, "o_auth_subject": snapshot.OAuthSubject}).Error)
	assert.EqualError(t, service.RefreshManagedGPTAccountGateway(context.Background(), snapshot), "gateway_import_failed")
	assert.Equal(t, 1, refreshes)
}

func TestGPTAccountGatewayCPARefreshUsesConfirmedRemoteContract(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	refreshes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"type":"codex","new_api_account":"owned-first","email":"first@example.com","account_id":"remote-first"}`))
			return
		}
		assert.Equal(t, "/v8/management/credentials/refresh", r.URL.Path)
		refreshes++
		_, _ = w.Write([]byte(`{"ok":true,"auth":{"id":"credential"}}`))
	}))
	defer upstream.Close()
	gateway := gatewayFixture(t, "cpa", upstream.URL, "v8")
	snapshot, _ := gatewayAccountFixture(t, identity, gateway, "first")
	snapshot.AccountID = "remote-first"
	secrets, err := service.DecryptGPTAccountSecrets(snapshot)
	require.NoError(t, err)
	secrets.GatewayRemoteID = "new-api-" + model.GPTChannelKeyHash(snapshot.SecretID)[:32] + ".json"
	require.NoError(t, service.EncryptGPTAccountSecrets(snapshot, *secrets))
	require.NoError(t, model.DB.Model(snapshot).Updates(map[string]any{"secrets_ciphertext": snapshot.SecretsCiphertext, "account_id": snapshot.AccountID, "o_auth_subject": snapshot.OAuthSubject}).Error)
	require.NoError(t, service.RefreshManagedGPTAccountGateway(context.Background(), snapshot))
	assert.Equal(t, 1, refreshes)
}

func TestGPTAccountGatewaySyncRequiresScopedOwnedAuthorization(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	gateway := gatewayFixture(t, "cpa", "http://127.0.0.1:1", "v8")
	account, _ := gatewayAccountFixture(t, identity, gateway, "first")
	for _, scope := range []string{"", "gpt_account.update"} {
		response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{}`, scope, SyncGPTAccountGateway)
		assert.Equal(t, http.StatusForbidden, response.Code)
	}
	require.NoError(t, model.DB.Model(account).Update("owner_id", identity.UserID+1).Error)
	response := gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodPost, `{}`, "", SyncGPTAccountGateway)
	assert.Equal(t, http.StatusNotFound, response.Code)
}
