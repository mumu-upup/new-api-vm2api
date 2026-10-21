package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	"gorm.io/gorm"
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
		effort   string
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
		{
			name:     "VM2API base URL",
			raw:      `{"provider":"vm2api","base_url":"https://vm2api.example/v1","api_key":"vm-key","model":"gpt-5"}`,
			provider: "vm2api",
			baseURL:  "https://vm2api.example",
			key:      "vm-key",
			models:   "gpt-5",
			group:    "",
		},
		{
			name:     "Prism bridge",
			raw:      `{"provider":"prism-bridge","base_url":"http://127.0.0.1:18765/v1","allow_local_http":true,"api_key":"local-key","model":"gpt-6.1-sol"}`,
			provider: "prism",
			baseURL:  "http://127.0.0.1:18765",
			key:      "local-key",
			models:   "gpt-6.1-sol",
			effort:   "max",
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
			assert.Equal(t, test.effort, got.ForcedReasoningEffort)
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

func TestCreateGPTGatewayChannelPersistsPrismReasoningPolicy(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	input, err := service.ParseGPTGatewayChannelJSON(`{"provider":"prism","base_url":"http://127.0.0.1:18765/v1","allow_local_http":true,"api_key":"relay-key","models":["gpt-6.1-sol"]}`)
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		effort string
		want   string
	}{
		{name: "default", want: "max"},
		{name: "explicit", effort: " HIGH ", want: "high"},
		{name: "disabled", effort: "none", want: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input.ForcedReasoningEffort = test.effort
			channel, err := service.CreateGPTGatewayChannel(input)
			require.NoError(t, err)
			require.NotZero(t, channel.Id)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.Equal(t, constant.ChannelTypeOpenAI, stored.Type)
			assert.Equal(t, "http://127.0.0.1:18765", stored.GetBaseURL())
			assert.Equal(t, test.want, stored.GetOtherSettings().ForcedReasoningEffort)
		})
	}
}

func TestCreateGPTGatewayChannelRejectsInvalidReasoningPolicy(t *testing.T) {
	setupGPTAccountGatewayTest(t)
	_, err := service.CreateGPTGatewayChannel(service.GPTGatewayChannelInput{
		Name:                  "Prism bridge",
		Provider:              "prism",
		BaseURL:               "http://127.0.0.1:18765",
		APIKey:                "relay-key",
		Models:                "gpt-6.1-sol",
		ForcedReasoningEffort: "ultra",
	})
	assert.Error(t, err)
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

func TestGPTAccountGatewayVM2APICreatesDeterministicCodexSlotAndReusesIt(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	var vmID string
	creates, imports, refreshes := 0, 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "management-secret", r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/panel/vms/"):
			if vmID == "" || r.URL.Path != "/api/panel/vms/"+vmID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex","has_token":true,"email":"first@example.com","chatgpt_account_id":"remote-first"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/create":
			creates++
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			vmID, _ = body["id"].(string)
			assert.NotEmpty(t, vmID)
			assert.Equal(t, "openai", body["platform"])
			assert.Equal(t, true, body["auto_allocate_proxy"])
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/import":
			imports++
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			assert.Equal(t, vmID, body["vm_id"])
			raw, ok := body["auth_json"].(string)
			require.True(t, ok)
			var account map[string]any
			require.NoError(t, common.Unmarshal([]byte(raw), &account))
			assert.Equal(t, "remote-first", account["chatgpt_account_id"])
			assert.Equal(t, "first@example.com", account["email"])
			assert.IsType(t, float64(0), account["expires_at"])
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/"+vmID+"/oauth/refresh":
			refreshes++
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			assert.Equal(t, vmID, body["vm_id"])
			_, _ = w.Write([]byte(`{"ok":true,"data":{"ok":true,"refreshed":true,"platform":"openai"}}`))
		default:
			t.Fatalf("unexpected vm2api request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()
	gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: "vm2api", ManagementURL: upstream.URL, RelayURL: upstream.URL, AllowLocalHTTP: true, ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
	key.AccountID = "remote-first"
	key.Email = snapshot.Email
	require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, imports)
	require.NoError(t, service.RefreshManagedGPTAccountGateway(context.Background(), snapshot))
	assert.Equal(t, 1, refreshes)
	service.ReleaseGPTAccountJob(snapshot, "")
	assert.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, imports)
}

func TestImportGPTAccountOAuthWithVM2APIGatewayCreatesManagedSlot(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	var vmID string
	imports := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "management-secret", r.Header.Get("x-api-key"))
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/panel/vms/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/create":
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			vmID, _ = body["id"].(string)
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/import":
			imports++
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			assert.Equal(t, vmID, body["vm_id"])
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex"},"platform":"openai"}}`))
		default:
			t.Fatalf("unexpected vm2api request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()
	gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: "vm2api", ManagementURL: upstream.URL, RelayURL: upstream.URL, AllowLocalHTTP: true, ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	raw := fmt.Sprintf(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":%q,"account_id":"remote-import","email":"import@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("import@example.com", "remote-import", "subject-import"))
	account, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, service.GPTAccountOAuthImportInput{OAuthJSON: raw, GatewayID: &gateway.ID})
	require.NoError(t, err)
	assert.Nil(t, account.ChannelID)
	assert.Equal(t, &gateway.ID, account.GatewayID)
	assert.Equal(t, "ready", account.Status)
	assert.Equal(t, 1, imports)
	var stored model.GPTAccount
	require.NoError(t, model.DB.First(&stored, account.ID).Error)
	assert.Nil(t, stored.ChannelID)
	assert.Equal(t, &gateway.ID, stored.GatewayID)
}

func TestGPTAccountGatewayVM2APIUncertainImportDoesNotReplayFreshOAuth(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	var vmID string
	creates, imports := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "management-secret", r.Header.Get("x-api-key"))
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/panel/vms/"):
			if vmID == "" || r.URL.Path != "/api/panel/vms/"+vmID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex","has_token":false,"has_refresh":false}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/create":
			creates++
			var body map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &body))
			vmID, _ = body["id"].(string)
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"` + vmID + `","platform":"openai","family":"codex"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/import":
			imports++
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Fatalf("unexpected vm2api request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()
	gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: "vm2api", ManagementURL: upstream.URL, RelayURL: upstream.URL, AllowLocalHTTP: true, ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	snapshot, key := gatewayAccountFixture(t, identity, gateway, "uncertain")
	err = service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
	require.EqualError(t, err, "gateway_import_uncertain")
	service.ReleaseGPTAccountJob(snapshot, err.Error())
	retry, err := service.AcquireGPTAccountJob(snapshot.ID, false)
	require.NoError(t, err)
	key.AccessToken = "fresh-access"
	key.RefreshToken = "fresh-refresh"
	err = service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), retry, key)
	assert.EqualError(t, err, "gateway_import_uncertain")
	service.ReleaseGPTAccountJob(retry, service.GPTAccountErrorCode(err))
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, imports)
}

func TestGPTAccountGatewayVM2APIRecoversUncertainSlotCreation(t *testing.T) {
	for _, immediate := range []bool{true, false} {
		t.Run(fmt.Sprintf("visible_immediately_%t", immediate), func(t *testing.T) {
			identity := setupGPTAccountGatewayTest(t)
			var vmID string
			visible, imported := false, false
			creates, imports := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet:
					if !visible {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":%q,"platform":"openai","family":"codex","has_token":%t,"email":"first@example.com","chatgpt_account_id":"remote-first"}}}`, vmID, imported)
				case r.URL.Path == "/api/panel/vms/create":
					creates++
					var body map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &body))
					vmID = body["id"].(string)
					visible = immediate
					// Provisioning outlives the HTTP request; no OAuth was sent.
					w.WriteHeader(http.StatusGatewayTimeout)
				case r.URL.Path == "/api/panel/vms/import":
					imports++
					imported = true
					fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":%q,"platform":"openai","family":"codex"}}}`, vmID)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()
			gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "Pool", Provider: "vm2api", ManagementURL: upstream.URL, RelayURL: upstream.URL, AllowLocalHTTP: true, ManagementKey: "management-secret", RelayKey: "relay-secret", Group: "default", Models: "gpt-5"})
			require.NoError(t, err)
			snapshot, key := gatewayAccountFixture(t, identity, gateway, "first")
			err = service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key)
			if immediate {
				require.NoError(t, err, "an existing empty slot can receive its first credential after create times out")
				service.ReleaseGPTAccountJob(snapshot, "")
			} else {
				require.Error(t, err)
				service.ReleaseGPTAccountJob(snapshot, service.GPTAccountErrorCode(err))
				var account model.GPTAccount
				require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
				secrets, decryptErr := service.DecryptGPTAccountSecrets(&account)
				require.NoError(t, decryptErr)
				assert.False(t, secrets.GatewayImportAttempted, "creating an empty slot must not consume the credential import attempt")
				visible = true
				require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
			}
			require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
			assert.Equal(t, 1, creates)
			assert.Equal(t, 1, imports)
		})
	}
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

func TestGPTAccountGatewayRootSessionAndSafeTargetList(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	body := `{"name":"Pool","provider":"cpa","management_version":"v8","management_url":"https://cpa.example","relay_url":"https://relay.example","management_key":"management-secret","relay_key":"relay-secret","group":"default","models":"gpt-5"}`
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		denied := gptAccountRequest(t, identity, role, 0, http.MethodPost, body, CreateGPTAccountGateway)
		assert.Equal(t, http.StatusForbidden, denied.Code)
	}
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, CreateGPTAccountGateway)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.NotContains(t, created.Body.String(), "management-secret")
	assert.NotContains(t, created.Body.String(), "relay-secret")
	var gateway model.GPTAccountGateway
	require.NoError(t, model.DB.First(&gateway).Error)
	updatedBody := strings.Replace(body, `"name":"Pool"`, `"name":"Updated pool"`, 1)
	updated := gptAccountRequest(t, identity, common.RoleRootUser, gateway.ID, http.MethodPut, updatedBody, UpdateGPTAccountGateway)
	require.Equal(t, http.StatusOK, updated.Code)
	require.NoError(t, model.DB.First(&gateway, gateway.ID).Error)
	assert.Equal(t, "Updated pool", gateway.Name)
	assert.NotContains(t, updated.Body.String(), "management-secret")
	assert.NotContains(t, updated.Body.String(), "relay-secret")
	for _, role := range []int{common.RoleAdminUser, common.RoleRootUser} {
		listed := gptAccountRequest(t, identity, role, 0, http.MethodGet, "", ListGPTAccountGateways)
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
	ordinary := gptAccountRequest(t, identity, common.RoleCommonUser, 0, http.MethodGet, "", ListGPTAccountGateways)
	assert.Equal(t, http.StatusForbidden, ordinary.Code)
	createdAccount := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, fmt.Sprintf(`{"name":"Pooled","email":"owner@example.com","gateway_id":%d}`, gateway.ID), CreateGPTAccount)
	require.Equal(t, http.StatusOK, createdAccount.Code, createdAccount.Body.String())
	assert.Contains(t, createdAccount.Body.String(), fmt.Sprintf(`"gateway_id":%d`, gateway.ID))
	assert.Contains(t, createdAccount.Body.String(), fmt.Sprintf(`"gateway_channel_id":%d`, *gateway.ChannelID))
	assert.Contains(t, createdAccount.Body.String(), `"channel_id":null`)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
	assert.Contains(t, listed.Body.String(), `"gateway_provider":"cpa"`)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	raw, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "management-secret")
	assert.NotContains(t, string(raw), "relay-secret")
}

func TestGPTGatewayChannelJSONImportRequiresRootSessionAndCreatesChannel(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	body := `{"config_json":"{\"provider\":\"sub2api\",\"name\":\"测试渠道\",\"base_url\":\"https://sub2api.example/v1\",\"api_key\":\"relay-secret\",\"models\":[\"gpt-5\"]}"}`
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		denied := gptAccountRequest(t, identity, role, 0, http.MethodPost, body, ImportGPTGatewayChannelJSON)
		assert.Equal(t, http.StatusForbidden, denied.Code)
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&count).Error)
	assert.Zero(t, count)
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, ImportGPTGatewayChannelJSON)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"provider":"sub2api"`)
	assert.NotContains(t, created.Body.String(), "relay-secret")
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

func TestGPTAccountGatewaySyncRequiresOwnedAdminSession(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	gateway := gatewayFixture(t, "cpa", "http://127.0.0.1:1", "v8")
	account, _ := gatewayAccountFixture(t, identity, gateway, "first")
	// A permitted request reaches the gateway service and reports its unavailable
	// connection, without prompting for a second password or a security proof.
	response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{}`, SyncGPTAccountGateway)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.NotContains(t, response.Body.String(), "SECURITY_PROOF")
	require.NoError(t, model.DB.Model(account).Update("owner_id", identity.UserID+1).Error)
	response = gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodPost, `{}`, SyncGPTAccountGateway)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

// This fixture uses a real Unix HTTP socket and separate HTTP listeners, so
// isolation, credential transport and recovery run through the production client.
type isolatedVM2APIFixture struct {
	guestStarts     int
	failNextVMRead  bool
	mu              sync.Mutex
	instances       map[string]string
	states          map[string]string
	slots           map[string]map[string]any
	failDelete      bool
	malformedDelete bool
	ensures         int
	imports         int
	packageImports  int
	ensureHook      func()
	importVMHook    func(map[string]any)
	startVMHook     func(map[string]any)
}

func isolatedVM2APIManagerFixture(t *testing.T) *isolatedVM2APIFixture {
	t.Helper()
	fixture := &isolatedVM2APIFixture{instances: map[string]string{}, states: map[string]string{}, slots: map[string]map[string]any{}}
	directory, err := os.MkdirTemp("/tmp", "vm2-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "manager.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		require.GreaterOrEqual(t, len(parts), 3)
		id := parts[2]
		if r.Method == http.MethodDelete {
			if fixture.malformedDelete {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			if fixture.failDelete {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error_code":"cleanup_failed","error":"secret"}`))
				return
			}
			delete(fixture.instances, id)
			_, _ = w.Write([]byte(`{"id":"` + id + `","status":"deleted"}`))
			return
		}
		endpoint := fixture.instances[id]
		if endpoint == "" && r.Method != http.MethodPut {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error_code":"instance_missing"}`))
			return
		}
		if endpoint == "" {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				assert.Equal(t, "management-"+id, r.Header.Get("x-api-key"))
				slotID := "new-api-" + id + "-isolated"
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/panel/vms/"+slotID:
					if fixture.failNextVMRead {
						fixture.failNextVMRead = false
						w.WriteHeader(503)
						return
					}

					slot := fixture.slots[id]
					if slot == nil {
						w.WriteHeader(404)
						return
					}
					raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"vm": slot}})
					_, _ = w.Write(raw)
				case r.URL.Path == "/api/panel/vms/create":
					var body map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &body))
					assert.Equal(t, slotID, body["id"])
					slot := map[string]any{"id": slotID, "platform": "openai", "family": "codex", "status": "running", "schedulable": false, "schedule_disabled_reason": "no_credential", "has_token": false}
					fixture.slots[id] = slot
					raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"vm": slot}})
					_, _ = w.Write(raw)
				case r.URL.Path == "/api/panel/vms/package":
					fixture.packageImports++
					var payload map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &payload))
					vm := payload["vm"].(map[string]any)
					assert.Equal(t, slotID, vm["id"])
					assert.Equal(t, "unchanged-name", vm["name"])
					accounts := payload["credential"].(map[string]any)["accounts"].([]any)
					require.Len(t, accounts, 1)
					credential := accounts[0].(map[string]any)
					fixture.slots[id] = map[string]any{"id": slotID, "platform": "openai", "family": "codex", "status": "error", "has_token": true, "email": credential["email"], "chatgpt_account_id": credential["chatgpt_account_id"]}
					w.WriteHeader(503) // Write succeeded before the response was lost.
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
					fixture.guestStarts++
					slot := fixture.slots[id]
					slot["status"] = "running"
					slot["schedulable"] = slot["has_token"] == true || slot["has_refresh"] == true
					if slot["schedulable"] == true {
						slot["schedule_disabled_reason"] = ""
					}
					if fixture.startVMHook != nil {
						fixture.startVMHook(slot)
					}
					raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"vm": fixture.slots[id]}})
					_, _ = w.Write(raw)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/schedulable"):
					var body map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &body))
					fixture.slots[id]["schedulable"] = body["schedulable"]
					_, _ = w.Write([]byte(`{"ok":true}`))
				case r.URL.Path == "/api/panel/vms/import":
					fixture.imports++
					var body map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &body))
					var credential map[string]any
					require.NoError(t, common.UnmarshalJsonStr(body["auth_json"].(string), &credential))
					slot := fixture.slots[id]
					slot["has_token"] = true
					slot["email"] = credential["email"]
					slot["chatgpt_account_id"] = credential["chatgpt_account_id"]
					if fixture.importVMHook != nil {
						fixture.importVMHook(slot)
					}
					raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"vm": slot}})
					_, _ = w.Write(raw)
				default:
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(upstream.Close)
			endpoint = upstream.URL
			fixture.instances[id] = endpoint
			fixture.states[id] = "running"
		}
		if r.Method == http.MethodPut {
			fixture.ensures++
			if fixture.ensureHook != nil {
				fixture.ensureHook()
			}
		}
		port, _ := strconv.Atoi(strings.TrimPrefix(endpoint, "http://127.0.0.1:"))
		if len(parts) == 4 {
			if parts[3] == "stop" {
				fixture.states[id] = "stopped"
			} else {
				fixture.states[id] = "running"
			}
		}
		body := map[string]any{"id": id, "status": fixture.states[id], "container_name": "vm2api-account-" + id, "data_directory": "/srv/instances/" + id, "image": "vm2api@sha256:test", "port": port, "management_url": endpoint, "relay_url": endpoint, "updated_at": time.Now().Unix()}
		if r.Method == http.MethodPut {
			body["management_key"] = "management-" + id
			body["relay_key"] = "relay-" + id
		}
		raw, _ := common.Marshal(body)
		_, _ = w.Write(raw)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("VM2API_INSTANCE_MANAGER_SOCKET", socket)
	t.Setenv("VM2API_INSTANCE_HOST", "127.0.0.1")
	return fixture
}

func TestVM2APIInstanceRestoresOnlyInitialCredentialPause(t *testing.T) {
	for _, tc := range []struct {
		name            string
		reason          string
		manual          bool
		wrongIdentity   bool
		wrongAfterStart bool
		failedReadback  bool
		stillPaused     bool
		wantError       string
		wantStarts      int
		wantSchedulable bool
	}{
		{name: "initial import and reconciliation", reason: "no_credential", wantStarts: 1, wantSchedulable: true},
		{name: "manual credential pause", reason: "no_credential", manual: true},
		{name: "explicit pause", reason: "disabled"},
		{name: "proxy failure", reason: "proxy_required"},
		{name: "foreign imported identity", reason: "no_credential", wrongIdentity: true, wantError: "gateway_import_uncertain"},
		{name: "identity changed during start", reason: "no_credential", wrongAfterStart: true, wantStarts: 1, wantSchedulable: true, wantError: "gateway_remote_conflict"},
		{name: "lost readback reconciles without replay", reason: "no_credential", failedReadback: true, wantStarts: 1, wantSchedulable: true, wantError: "instance_not_running"},
		{name: "unschedulable readback", reason: "no_credential", stillPaused: true, wantStarts: 1, wantError: "instance_not_running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := setupGPTAccountGatewayTest(t)
			fixture := isolatedVM2APIManagerFixture(t)
			fixture.importVMHook = func(slot map[string]any) {
				slot["schedule_disabled_reason"] = tc.reason
				slot["schedule_manual"] = tc.manual
				if tc.wrongIdentity {
					slot["email"] = "another-account@example.com"
				}
			}
			fixture.startVMHook = func(slot map[string]any) {
				if tc.wrongAfterStart {
					slot["chatgpt_account_id"] = "another-account"
				}
				if tc.failedReadback {
					fixture.failNextVMRead = true
				}
				if tc.stillPaused {
					slot["schedulable"] = false
				}
			}
			template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "template", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "shared-management", RelayKey: "shared-relay", Group: "default", Models: "gpt-5"})
			require.NoError(t, err)
			snapshot, key := gatewayAccountFixture(t, identity, template, "schedule-recovery")
			require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
			service.ReleaseGPTAccountJob(snapshot, "")
			err = service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tc.wantError)
			}
			var instance model.GPTAccountVM2APIInstance
			require.NoError(t, model.DB.First(&instance, "account_id = ?", snapshot.ID).Error)
			var gateway model.GPTAccountGateway
			require.NoError(t, model.DB.First(&gateway, *instance.GatewayID).Error)
			var channel model.Channel
			require.NoError(t, model.DB.First(&channel, *gateway.ChannelID).Error)
			if tc.wantError != "" {
				assert.Equal(t, common.ChannelStatusAutoDisabled, channel.Status)
				assert.Equal(t, "failed", instance.Status)
			}
			fixture.mu.Lock()
			assert.Equal(t, tc.wantStarts, fixture.guestStarts)
			assert.Equal(t, tc.wantSchedulable, fixture.slots[instance.InstanceID]["schedulable"])
			assert.Equal(t, tc.manual, fixture.slots[instance.InstanceID]["schedule_manual"])
			assert.Equal(t, 1, fixture.imports)
			if tc.name == "initial import and reconciliation" {
				fixture.slots[instance.InstanceID]["schedulable"] = false
				fixture.slots[instance.InstanceID]["schedule_disabled_reason"] = "no_credential"
			}
			fixture.startVMHook = nil
			fixture.mu.Unlock()
			if tc.failedReadback || tc.name == "initial import and reconciliation" {
				require.NoError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID))
				fixture.mu.Lock()
				assert.Equal(t, 1, fixture.imports, "recovery must not replay credentials")
				assert.Equal(t, true, fixture.slots[instance.InstanceID]["schedulable"])
				fixture.mu.Unlock()
				require.NoError(t, model.DB.First(&channel, channel.Id).Error)
				assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
			}
		})
	}
}

func TestVM2APIInstanceIsolationRecoveryAndCleanup(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountVM2APIInstance{}))
	fixture := isolatedVM2APIManagerFixture(t)
	template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "template", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "shared-management", RelayKey: "shared-relay", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.Option{Key: model.GPTAccountDefaultGatewayKey, Value: strconv.Itoa(template.ID)}).Error)
	var gateways []int
	for _, suffix := range []string{"isolated-first", "isolated-second"} {
		snapshot, key := gatewayAccountFixture(t, identity, template, suffix)
		require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
		service.ReleaseGPTAccountJob(snapshot, "")
		require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
		require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
		var account model.GPTAccount
		require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
		require.NotNil(t, account.GatewayID)
		assert.NotEqual(t, template.ID, *account.GatewayID)
		gateways = append(gateways, *account.GatewayID)
		var gateway model.GPTAccountGateway
		require.NoError(t, model.DB.First(&gateway, *account.GatewayID).Error)
		require.NotNil(t, gateway.ManagedAccountID)
		assert.Equal(t, account.ID, *gateway.ManagedAccountID)
		var channel model.Channel
		require.NoError(t, model.DB.First(&channel, *gateway.ChannelID).Error)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		require.NoError(t, service.ManageGPTAccountVM2APIInstance(context.Background(), account.ID, "stop"))
		require.NoError(t, model.DB.First(&channel, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusAutoDisabled, channel.Status)
		stopped, err := service.GetGPTAccountVM2APIInstance(context.Background(), account.ID)
		require.NoError(t, err)
		assert.Equal(t, "stopped", stopped.RuntimeStatus)
		require.NoError(t, service.ManageGPTAccountVM2APIInstance(context.Background(), account.ID, "start"))
		require.NoError(t, model.DB.First(&channel, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		fixture.mu.Lock()
		fixture.failNextVMRead = true
		fixture.mu.Unlock()
		assert.Error(t, service.ManageGPTAccountVM2APIInstance(context.Background(), account.ID, "restart"))
		var interrupted model.GPTAccountVM2APIInstance
		require.NoError(t, model.DB.First(&interrupted, "account_id = ?", account.ID).Error)
		assert.Equal(t, "failed", interrupted.Status, "a slow control-plane restart must remain retryable")
		require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), account.ID))
		require.NoError(t, model.DB.First(&channel, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		_, err = service.SaveGPTAccountGateway(&gateway, service.GPTAccountGatewayInput{Name: "reassign"})
		assert.EqualError(t, err, "gateway_immutable")
		assert.Equal(t, "ready", account.Status)
		id := model.GPTChannelKeyHash(account.SecretID)[:32]
		fixture.mu.Lock()
		fixture.slots[id]["status"] = "stopped"
		fixture.mu.Unlock()
		require.NoError(t, model.DB.Model(&model.GPTAccountVM2APIInstance{}).Where("account_id = ?", account.ID).Update("status", "failed").Error)
		require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), account.ID), "retry must recover a persisted slot whose initial runtime startup failed")
		require.NoError(t, service.RequestGPTAccountVM2APIInstance(context.Background(), account.ID))
		require.NoError(t, model.DB.First(&account, account.ID).Error)
		assert.Equal(t, "ready", account.Status, "an idempotent ensure request must not leave a login running")
		other := model.GPTAccount{SecretID: "other"}
		assert.EqualError(t, service.ApplyGPTAccountGateway(&other, account.GatewayID), "gateway_immutable")
		status, err := service.GetGPTAccountVM2APIInstance(context.Background(), account.ID)
		require.NoError(t, err)
		encoded, err := common.Marshal(status)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "management-")
		assert.NotContains(t, string(encoded), "relay-")
		if suffix == "isolated-first" {
			fixture.mu.Lock()
			fixture.malformedDelete = true
			fixture.mu.Unlock()
			assert.EqualError(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID), "instance_invalid_response")
			require.NoError(t, model.DB.First(&account, account.ID).Error)
			fixture.mu.Lock()
			fixture.malformedDelete = false
			fixture.mu.Unlock()
			fixture.mu.Lock()
			fixture.failDelete = true
			fixture.mu.Unlock()
			assert.Error(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID))
			require.NoError(t, model.DB.First(&account, account.ID).Error)
			assert.Equal(t, "pending_cleanup", account.Status)
			require.NoError(t, model.DB.First(&channel, channel.Id).Error)
			assert.NotEqual(t, common.ChannelStatusEnabled, channel.Status)
			fixture.mu.Lock()
			fixture.failDelete = false
			fixture.mu.Unlock()
			require.NoError(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID))
			var count int64
			require.NoError(t, model.DB.Model(&model.GPTAccount{}).Where("id = ?", account.ID).Count(&count).Error)
			assert.Zero(t, count)
		}
	}
	assert.NotEqual(t, gateways[0], gateways[1])
	fixture.mu.Lock()
	assert.Equal(t, 2, fixture.imports)
	fixture.mu.Unlock()
	var source model.GPTAccountGateway
	require.NoError(t, model.DB.First(&source, template.ID).Error)
	defaultGateway, err := model.ResolveGPTAccountDefaultGateway()
	require.NoError(t, err)
	assert.Equal(t, &template.ID, defaultGateway)
}

func TestVM2APIInstanceCleanupAfterChannelDeletion(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	fixture := isolatedVM2APIManagerFixture(t)
	template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "template", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "shared-management", RelayKey: "shared-relay", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	accounts := make([]model.GPTAccount, 0, 2)
	instances := make([]model.GPTAccountVM2APIInstance, 0, 2)
	for _, suffix := range []string{"cleanup-target", "cleanup-unrelated"} {
		snapshot, key := gatewayAccountFixture(t, identity, template, suffix)
		require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
		service.ReleaseGPTAccountJob(snapshot, "")
		require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
		var account model.GPTAccount
		require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
		accounts = append(accounts, account)
		var instance model.GPTAccountVM2APIInstance
		require.NoError(t, model.DB.First(&instance, "account_id = ?", account.ID).Error)
		instances = append(instances, instance)
	}
	account, instance := accounts[0], instances[0]
	var gateway model.GPTAccountGateway
	require.NoError(t, model.DB.First(&gateway, *instance.GatewayID).Error)
	require.NoError(t, model.DB.Delete(&model.Channel{}, *gateway.ChannelID).Error)
	assert.ErrorIs(t, service.ManageGPTAccountVM2APIInstance(context.Background(), account.ID, "stop"), gorm.ErrRecordNotFound, "ordinary lifecycle actions still require their routing channel")

	// Cleanup must retain the same job and resource ownership fences even when
	// the channel has already gone away.
	require.NoError(t, model.DB.Model(&account).Update("lease_until", time.Now().Add(time.Minute).Unix()).Error)
	assert.EqualError(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID), "account_busy")
	require.NoError(t, model.DB.Model(&account).Update("lease_until", 0).Error)
	require.NoError(t, model.DB.Model(&gateway).Update("managed_account_id", accounts[1].ID+1).Error)
	assert.ErrorIs(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID), model.ErrGPTAccountStale)
	require.NoError(t, model.DB.Model(&gateway).Update("managed_account_id", account.ID).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("status", common.UserStatusDisabled).Error)
	assert.EqualError(t, service.DeleteGPTAccountWithInstance(context.Background(), account.ID), "authorization_forbidden")
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("status", common.UserStatusEnabled).Error)
	fixture.mu.Lock()
	assert.Contains(t, fixture.instances, instance.InstanceID)
	assert.Equal(t, "running", fixture.states[instance.InstanceID])
	fixture.failDelete = true
	fixture.mu.Unlock()

	failed := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodDelete, "", DeleteGPTAccount)
	require.Equal(t, http.StatusConflict, failed.Code, failed.Body.String())
	assert.Contains(t, failed.Body.String(), "instance_unavailable", "cleanup must reach the VM manager despite the absent channel")
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, "pending_cleanup", account.Status)
	require.NoError(t, model.DB.First(&instance, "account_id = ?", account.ID).Error)
	assert.Equal(t, "pending_cleanup", instance.Status)
	fixture.mu.Lock()
	assert.Contains(t, fixture.instances, instance.InstanceID)
	fixture.failDelete = false
	fixture.mu.Unlock()

	deleted := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodDelete, "", DeleteGPTAccount)
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	assert.ErrorIs(t, model.DB.First(&model.GPTAccount{}, account.ID).Error, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, model.DB.First(&model.GPTAccountVM2APIInstance{}, "account_id = ?", account.ID).Error, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, model.DB.First(&model.GPTAccountGateway{}, gateway.ID).Error, gorm.ErrRecordNotFound)
	var abilities int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", *gateway.ChannelID).Count(&abilities).Error)
	assert.Zero(t, abilities)
	fixture.mu.Lock()
	assert.NotContains(t, fixture.instances, instance.InstanceID)
	assert.Contains(t, fixture.instances, instances[1].InstanceID)
	fixture.mu.Unlock()
	var unrelated model.GPTAccount
	require.NoError(t, model.DB.First(&unrelated, accounts[1].ID).Error)
	assert.Equal(t, accounts[1], unrelated)
	for _, gatewayID := range []int{template.ID, *instances[1].GatewayID} {
		var preserved model.GPTAccountGateway
		require.NoError(t, model.DB.First(&preserved, gatewayID).Error)
		var channel model.Channel
		require.NoError(t, model.DB.First(&channel, *preserved.ChannelID).Error)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	}
}

func TestVM2APIMonitoringFiltersSecretsAndForeignSlots(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	const slotID = "new-api-safe"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/panel/vms/") {
			_, _ = w.Write([]byte(`{"ok":true,"data":{"vm":{"id":"new-api-safe","platform":"openai","family":"codex","status":"running","email":"monitor@example.com","access_token":"unsafe-token","proxy":{"password":"unsafe-proxy"},"codex_usage":{"quota":{"utilization_5h":12,"access_token":"nested-token"}},"utilization_7d":34,"schedulable":true}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"config":{"key":"secret-config"},"items":[{"id":"owned","vm_id":"new-api-safe","first_token_ms":123,"duration_ms":456,"api_key_presented":"unsafe-key","error_message":"Authorization: Bearer unsafe-error","error_code":"rate_limit"},{"id":"foreign","vm_id":"other","duration_ms":9}],"total":2}`))
	}))
	defer upstream.Close()
	gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "monitor", Provider: "vm2api", ManagementURL: upstream.URL, RelayURL: upstream.URL, AllowLocalHTTP: true, ManagementKey: "key", RelayKey: "key", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	account := model.GPTAccount{OwnerID: identity.UserID, Email: "monitor@example.com", GatewayID: &gateway.ID, SecretID: "monitor", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{GatewayRemoteID: slotID}))
	require.NoError(t, model.DB.Create(&account).Error)
	status, err := service.GetGPTAccountVM2APIStatus(context.Background(), account.ID)
	require.NoError(t, err)
	encoded, err := common.Marshal(status)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "unsafe")
	assert.NotContains(t, string(encoded), "nested-token")
	assert.Equal(t, float64(12), status.Quota["utilization_5h"])
	logs, err := service.ListGPTAccountVM2APILogs(context.Background(), account.ID, 50)
	require.NoError(t, err)
	encoded, err = common.Marshal(logs)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "unsafe")
	assert.NotContains(t, string(encoded), "foreign")
	assert.NotContains(t, string(encoded), "secret-config")
	assert.Contains(t, string(encoded), `"first_token_ms":123`)
	assert.Contains(t, string(encoded), `"duration_ms":456`)
	assert.EqualError(t, service.UpdateGPTAccountVM2APIPolicy(context.Background(), account.ID, map[string]any{"machine": map[string]any{"memory": "16g"}}), "invalid_gateway")
}

func TestVM2APIInstanceMigrationStopsSourceAndRecoversAmbiguousImport(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountVM2APIInstance{}))
	fixture := isolatedVM2APIManagerFixture(t)
	stopped := false
	sourceSlot := "new-api-" + model.GPTChannelKeyHash("owned-migration")[:32]
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/panel/vms/"+sourceSlot+"/stop":
			stopped = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/panel/vms/"+sourceSlot+"/package":
			assert.True(t, stopped, "source must stop before credentials leave its runtime")
			raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"type": "vm2api-vm-package", "version": 1, "vm": map[string]any{"id": sourceSlot, "name": "unchanged-name", "platform": "openai", "family": "codex", "schedulable": false}, "credential": map[string]any{"platform": "openai", "accounts": []any{map[string]any{"email": "migration@example.com", "chatgpt_account_id": "remote-migration", "access_token": "rotated-access", "refresh_token": "rotated-refresh"}}}}})
			_, _ = w.Write(raw)
		default:
			status := "running"
			if stopped {
				status = "stopped"
			}
			raw, _ := common.Marshal(map[string]any{"ok": true, "data": map[string]any{"vm": map[string]any{"id": sourceSlot, "platform": "openai", "family": "codex", "status": status, "schedulable": !stopped, "has_token": true, "email": "migration@example.com", "chatgpt_account_id": "remote-migration"}}})
			_, _ = w.Write(raw)
		}
	}))
	defer source.Close()
	template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "shared", Provider: "vm2api", ManagementURL: source.URL, RelayURL: source.URL, AllowLocalHTTP: true, ManagementKey: "source-key", RelayKey: "source-key", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	snapshot, key := gatewayAccountFixture(t, identity, template, "migration")
	snapshot.AccountID = key.AccountID
	require.NoError(t, service.EncryptGPTAccountSecrets(snapshot, service.GPTAccountSecrets{GatewayRemoteID: sourceSlot}))
	require.NoError(t, model.DB.Model(&model.GPTAccount{}).Where("id = ?", snapshot.ID).Updates(map[string]any{"account_id": key.AccountID, "o_auth_subject": snapshot.OAuthSubject, "secrets_ciphertext": snapshot.SecretsCiphertext, "gateway_sync_status": "synced", "status": "ready"}).Error)
	service.ReleaseGPTAccountJob(snapshot, "")
	require.NoError(t, service.RequestGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
	require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
	require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
	assert.True(t, stopped)
	fixture.mu.Lock()
	assert.Equal(t, 1, fixture.packageImports)
	assert.Equal(t, 1, fixture.guestStarts, "persisted package with failed startup must recover without package replay")
	assert.Zero(t, fixture.imports)
	fixture.mu.Unlock()
	var instance model.GPTAccountVM2APIInstance
	require.NoError(t, model.DB.First(&instance, "account_id = ?", snapshot.ID).Error)
	assert.Equal(t, "ready", instance.Status)
	assert.True(t, instance.SourcePaused)
	assert.Equal(t, "running", instance.SourceStatus)
	assert.True(t, instance.SourceSchedulable)
	assert.Equal(t, template.ID, instance.SourceGatewayID)
	assert.Empty(t, instance.PackageCiphertext)
	assert.NotContains(t, instance.SourceSecretsCiphertext, "rotated")
	// Once migration completes, user scheduling changes override the historical
	// source policy during lifecycle operations and their recovery retries.
	require.NoError(t, service.SetGPTAccountVM2APISchedulable(context.Background(), snapshot.ID, false))
	for _, action := range []string{"restart", "rebuild"} {
		require.NoError(t, service.ManageGPTAccountVM2APIInstance(context.Background(), snapshot.ID, action))
		fixture.mu.Lock()
		assert.Equal(t, false, fixture.slots[instance.InstanceID]["schedulable"], action+" must preserve the current scheduling policy")
		fixture.mu.Unlock()
	}
	fixture.mu.Lock()
	fixture.failNextVMRead = true
	fixture.mu.Unlock()
	require.Error(t, service.ManageGPTAccountVM2APIInstance(context.Background(), snapshot.ID, "restart"))
	require.NoError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID))
	fixture.mu.Lock()
	assert.Equal(t, false, fixture.slots[instance.InstanceID]["schedulable"], "retry must preserve the current scheduling policy")
	assert.Equal(t, 1, fixture.packageImports, "completed migration must never replay the credential package")
	fixture.mu.Unlock()
	var sourceChannel model.Channel
	require.NoError(t, model.DB.First(&sourceChannel, *template.ChannelID).Error)
	assert.Equal(t, common.ChannelStatusEnabled, sourceChannel.Status, "other source accounts keep their shared channel")
}

func TestVM2APIInstanceLeaseRevocationAndAdminBoundary(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountVM2APIInstance{}))
	fixture := isolatedVM2APIManagerFixture(t)
	template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "shared", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "key", RelayKey: "key", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	snapshot, key := gatewayAccountFixture(t, identity, template, "fenced")
	require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
	assert.EqualError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID), "account_busy")
	assert.EqualError(t, service.DeleteGPTAccountWithInstance(context.Background(), snapshot.ID), "account_busy")
	assert.EqualError(t, service.SetGPTAccountVM2APISchedulable(context.Background(), snapshot.ID, true), "account_busy")
	service.ReleaseGPTAccountJob(snapshot, "")
	denied := gptAccountRequest(t, identity, common.RoleCommonUser, snapshot.ID, http.MethodGet, "", GetGPTAccountVM2APIInstance)
	assert.Equal(t, http.StatusForbidden, denied.Code)
	fixture.ensureHook = func() {
		require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", snapshot.OwnerID).Update("status", common.UserStatusDisabled).Error)
	}
	assert.ErrorIs(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID), model.ErrGPTAccountStale)
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	assert.Equal(t, &template.ID, account.GatewayID)
	fixture.mu.Lock()
	assert.Zero(t, fixture.imports)
	fixture.mu.Unlock()
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", snapshot.OwnerID).Update("status", common.UserStatusEnabled).Error)
	fixture.ensureHook = nil
	t.Setenv("VM2API_INSTANCE_MANAGER_SOCKET", "/tmp/nonexistent-vm2api-manager.sock")
	status, err := service.GetGPTAccountVM2APIInstance(context.Background(), snapshot.ID)
	require.NoError(t, err)
	assert.Equal(t, "unavailable", status.RuntimeStatus)
}

func TestVM2APIInstanceRetryNeverFallsBackToSharedGateway(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	fixture := isolatedVM2APIManagerFixture(t)
	socket := os.Getenv("VM2API_INSTANCE_MANAGER_SOCKET")
	template, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "shared", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "key", RelayKey: "key", Group: "default", Models: "gpt-5"})
	require.NoError(t, err)
	snapshot, key := gatewayAccountFixture(t, identity, template, "retry")
	require.NoError(t, service.StoreVerifiedGPTAccountGatewayCredential(context.Background(), snapshot, key))
	service.ReleaseGPTAccountJob(snapshot, "")
	require.NoError(t, model.DB.Where("account_id = ?", snapshot.ID).Delete(&model.GPTAccountVM2APIInstance{}).Error)
	assert.EqualError(t, service.SyncGPTAccountGateway(context.Background(), snapshot.ID), "instance_not_ready")

	t.Setenv("VM2API_INSTANCE_MANAGER_SOCKET", "/tmp/nonexistent-vm2api-manager.sock")
	assert.EqualError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID), "instance_unavailable")
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	require.NotNil(t, secrets.GatewayOAuth)
	assert.Equal(t, key.RefreshToken, secrets.GatewayOAuth.RefreshToken)
	var instance model.GPTAccountVM2APIInstance
	require.NoError(t, model.DB.First(&instance, "account_id = ?", snapshot.ID).Error)
	assert.Equal(t, "failed", instance.Status)
	t.Setenv("VM2API_INSTANCE_MANAGER_SOCKET", socket)
	entered, release := make(chan struct{}), make(chan struct{})
	fixture.ensureHook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID) }()
	<-entered
	assert.EqualError(t, service.EnsureGPTAccountVM2APIInstance(context.Background(), snapshot.ID), "account_busy")
	close(release)
	require.NoError(t, <-done)
	fixture.mu.Lock()
	assert.Equal(t, 1, fixture.imports)
	fixture.mu.Unlock()
	require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
	assert.Equal(t, "ready", account.Status)
}

func TestVM2APIInstanceDatabaseMigrationPreservesSharedBindings(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("upgrade=%v", upgrade), func(t *testing.T) {
			identity := setupGPTAccountGatewayTest(t)
			gateway, err := service.SaveGPTAccountGateway(nil, service.GPTAccountGatewayInput{Name: "legacy shared", Provider: "vm2api", ManagementURL: "https://shared.example", RelayURL: "https://shared.example", ManagementKey: "management", RelayKey: "relay", Group: "default", Models: "gpt-5"})
			require.NoError(t, err)
			snapshot, _ := gatewayAccountFixture(t, identity, gateway, "migration-schema")
			service.ReleaseGPTAccountJob(snapshot, "")
			if upgrade {
				// This is the exact c76cc8951 gateway schema: all existing columns and
				// indexes, without the newly introduced managed owner column/table.
				require.NoError(t, model.DB.Migrator().DropIndex(&model.GPTAccountGateway{}, "idx_gpt_account_gateways_managed_account_id"))
				require.NoError(t, model.DB.Migrator().DropColumn(&model.GPTAccountGateway{}, "ManagedAccountID"))
				require.NoError(t, model.DB.Migrator().DropTable(&model.GPTAccountVM2APIInstance{}))
			}
			for range 2 {
				require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountGateway{}, &model.GPTAccountVM2APIInstance{}, &model.GPTAccount{}))
			}
			var preserved model.GPTAccountGateway
			require.NoError(t, model.DB.First(&preserved, gateway.ID).Error)
			assert.Nil(t, preserved.ManagedAccountID)
			assert.Equal(t, gateway.ChannelID, preserved.ChannelID)
			assert.Equal(t, gateway.SecretsCiphertext, preserved.SecretsCiphertext)
			var account model.GPTAccount
			require.NoError(t, model.DB.First(&account, snapshot.ID).Error)
			assert.Equal(t, &gateway.ID, account.GatewayID)
			instance := model.GPTAccountVM2APIInstance{AccountID: account.ID, InstanceID: strings.Repeat("a", 32), SourceGatewayID: gateway.ID, Status: "pending"}
			require.NoError(t, model.DB.Create(&instance).Error)
			duplicate := instance
			duplicate.AccountID++
			assert.Error(t, model.DB.Create(&duplicate).Error, "instance IDs must remain unique")
			duplicate = instance
			duplicate.InstanceID = strings.Repeat("b", 32)
			assert.Error(t, model.DB.Create(&duplicate).Error, "account may only own one instance")
			for range 2 {
				require.NoError(t, model.DB.AutoMigrate(&model.GPTAccountGateway{}, &model.GPTAccountVM2APIInstance{}, &model.GPTAccount{}))
			}
			require.NoError(t, model.DB.First(&instance, "account_id = ?", account.ID).Error)
			assert.Equal(t, "pending", instance.Status)
		})
	}
}

func TestVM2APIIsolationAllowsDirectAccountsAndOAuthImports(t *testing.T) {
	identity := setupGPTAccountGatewayTest(t)
	fixture := isolatedVM2APIManagerFixture(t)
	direct := 0
	for _, gatewayID := range []*int{nil, &direct} {
		account := model.GPTAccount{}
		require.NoError(t, service.ApplyGPTAccountGateway(&account, gatewayID))
		assert.Nil(t, account.GatewayID)
	}
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Direct","email":"direct@example.com","password":"fixture-password","models":"gpt-5","group":"default"}`, CreateGPTAccount)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var directAccount model.GPTAccount
	require.NoError(t, model.DB.Where("email = ?", "direct@example.com").First(&directAccount).Error)
	assert.Nil(t, directAccount.GatewayID)
	raw := fmt.Sprintf(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":%q,"account_id":"no-template","email":"none@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("none@example.com", "no-template", "subject"))
	imported, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, service.GPTAccountOAuthImportInput{OAuthJSON: raw})
	require.NoError(t, err)
	assert.Nil(t, imported.GatewayID)
	assert.NotNil(t, imported.ChannelID)
	var count int64
	require.NoError(t, model.DB.Model(&model.GPTAccountVM2APIInstance{}).Count(&count).Error)
	assert.Zero(t, count)
	fixture.mu.Lock()
	assert.Zero(t, fixture.ensures)
	fixture.mu.Unlock()
}
