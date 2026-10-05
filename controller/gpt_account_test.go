package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGPTAccountRecoveryPreservesConcurrentAndStaleCredentials(t *testing.T) {
	identity := setupGPTAccountTest(t)
	channel := model.Channel{Type: 57, Key: `{"access_token":"original","account_id":"account"}`, Name: "Recovery", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	account := model.GPTAccount{OwnerID: identity.UserID, ChannelID: &channel.Id, ChannelKeyHash: model.GPTChannelKeyHash(channel.Key), SecretID: "recovery", Generation: 1, AutoRelogin: true, Status: "ready"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{}))
	require.NoError(t, model.DB.Create(&account).Error)
	job, err := service.AcquireGPTAccountJob(account.ID, true)
	require.NoError(t, err)
	require.NoError(t, model.PauseGPTAccountChannel(job))
	assert.True(t, service.ScheduleGPTAccountRecovery(channel.Id, channel.Key), "a competing 401 must not let ordinary auto-ban replace the recovery reason")
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer upstream.Close()
			for _, websocket := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: 57, ChannelBaseUrl: upstream.URL, ApiKey: channel.Key}}
				if websocket {
					_, err := relaychannel.DoWssRequest(&codex.Adaptor{}, c, info, nil)
					require.Error(t, err)
				} else {
					response, err := (&codex.Adaptor{}).DoRequest(c, info, strings.NewReader(`{}`))
					require.NoError(t, err)
					require.NoError(t, response.(*http.Response).Body.Close())
				}
				if status == http.StatusUnauthorized {
					assert.Equal(t, channel.Id, c.GetInt("gpt_account_recovery_channel_id"), "websocket=%v", websocket)
				} else {
					assert.Zero(t, c.GetInt("gpt_account_recovery_channel_id"))
				}
			}
		})
	}
	require.NoError(t, model.StoreGPTAccountCredential(job, `{"access_token":"fresh","account_id":"account"}`, "account"))
	service.ReleaseGPTAccountJob(job, "")
	assert.True(t, service.ScheduleGPTAccountRecovery(channel.Id, channel.Key), "late 401 for old key must not disable fresh credential")
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	assert.Contains(t, channel.Key, "fresh")
}

func TestGPTAccountWorkerAttentionStopsAutomaticRetries(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "attention", Generation: 1, AutoRelogin: true}
	require.NoError(t, model.DB.Create(&account).Error)
	for _, code := range []string{"captcha_required", "email_verification_required", "consent_required", "additional_verification_required", "invalid_credentials", "account_unavailable", "account_disabled", "login_timeout", "invalid_callback", "identity_verification_failed"} {
		t.Run(code, func(t *testing.T) {
			job, err := service.AcquireGPTAccountJob(account.ID, false)
			require.NoError(t, err)
			service.ReleaseGPTAccountJob(job, code)
			require.NoError(t, model.DB.First(&account, account.ID).Error)
			assert.Equal(t, "needs_attention", account.Status)
			assert.Equal(t, code, account.LastErrorCode)
			require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"next_retry_at": 0, "failure_count": 0}).Error)
			_, err = service.AcquireGPTAccountJob(account.ID, true)
			assert.Error(t, err)
		})
	}
	assert.Equal(t, "operation_failed", service.GPTAccountErrorCode(errors.New("secret response payload")))
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	require.NotEmpty(t, audits)
	for _, audit := range audits {
		assert.Equal(t, common.RoleRootUser, audit.ActorRole)
	}
}

func TestGPTAccountCredentialTamperingAndProofScope(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Original", Email: "owner@example.com", SecretID: "first", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
	require.NoError(t, model.DB.Create(&account).Error)
	copy := account
	copy.SecretID = "different-account"
	_, err := service.DecryptGPTAccountSecrets(&copy)
	assert.Error(t, err, "ciphertext must be bound to its account")
	copy = account
	copy.SecretsCiphertext = "v1.AAAA"
	_, err = service.DecryptGPTAccountSecrets(&copy)
	assert.Error(t, err)
	body := `{"name":"Changed","email":"owner@example.com"}`
	wrongScope := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, body, "gpt_account.delete", UpdateGPTAccount)
	assert.Equal(t, http.StatusForbidden, wrongScope.Code)
	wrongAccount := gptAccountRequest(t, identity, common.RoleRootUser, account.ID+1, http.MethodPut, body, "gpt_account.update", func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(account.ID)}}
		UpdateGPTAccount(c)
	})
	assert.Equal(t, http.StatusForbidden, wrongAccount.Code)
	missingSession := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, body, "gpt_account.update", func(c *gin.Context) {
		c.Set("session_id", "")
		UpdateGPTAccount(c)
	})
	assert.Equal(t, http.StatusForbidden, missingSession.Code)
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, "Original", account.Name)
	for _, callback := range []string{"https://localhost:1455/auth/callback?code=c&state=s", "http://attacker.invalid/auth/callback?code=c&state=s", "http://localhost:1455/auth/callback?code=c&code=d&state=s", "http://localhost:1455/auth/callback?code=c&state=s&state=x", "http://user@localhost:1455/auth/callback?code=c&state=s", "http://localhost:1455/auth/callback?code=c&state=s#fragment"} {
		assert.Error(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, identity, callback))
	}
	keyFile := os.Getenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE")
	require.NoError(t, os.Chmod(keyFile, 0644))
	_, err = service.DecryptGPTAccountSecrets(&account)
	assert.ErrorIs(t, err, service.ErrGPTAccountEncryption)
}

func TestGPTAccountWorkerInputLimits(t *testing.T) {
	identity := setupGPTAccountTest(t)
	for _, input := range []map[string]string{{"password": strings.Repeat("p", 1025)}, {"totp_secret": strings.Repeat("A", 160)}, {"totp_secret": "AAAAAAAAAAAAAAAAAB"}} {
		input["name"], input["email"] = "Invalid worker input", "owner@example.com"
		body, err := common.Marshal(input)
		require.NoError(t, err)
		response := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, string(body), "gpt_account.create", CreateGPTAccount)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
}

func TestGPTAccountTokenTransportNeverFollowsRedirects(t *testing.T) {
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var hosts []string
		client.Transport = gptOAuthTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
			hosts = append(hosts, request.URL.Host)
			if request.URL.Host == "auth.openai.com" {
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://other-public.example/token"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"rotated","expires_in":3600}`))}, nil
		}}
		_, err := service.RefreshCodexOAuthToken(context.Background(), "private-refresh")
		assert.Error(t, err)
		assert.Equal(t, []string{"auth.openai.com"}, hosts)
	}
}

func TestGPTAccountOAuthRequiresVerifiedTLS(t *testing.T) {
	previous := common.TLSInsecureSkipVerify
	common.TLSInsecureSkipVerify = true
	t.Cleanup(func() { common.TLSInsecureSkipVerify = previous })
	var calls atomic.Int32
	untrustedProxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer untrustedProxy.Close()
	_, err := service.RefreshCodexOAuthTokenWithProxy(context.Background(), "private-refresh", untrustedProxy.URL)
	assert.Error(t, err)
	assert.Zero(t, calls.Load(), "OAuth must reject an untrusted TLS certificate even when relay TLS checks are disabled")
}

func TestGPTAccountWorkerRequiresVerifiedTLS(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Email: "owner@example.com", SecretID: "worker-tls", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password"}))
	require.NoError(t, model.DB.Create(&account).Error)
	previousTransport := http.DefaultTransport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Simulate the legacy relay compatibility setting.
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport; transport.CloseIdleConnections() })
	var calls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer worker.Close()
	tokenFile := filepath.Join(t.TempDir(), "worker-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600))
	t.Setenv("GPT_ACCOUNT_WORKER_URL", worker.URL)
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", tokenFile)
	require.NoError(t, service.StartGPTAccountLogin(account.ID, identity, false))
	require.Eventually(t, func() bool {
		var updated model.GPTAccount
		return model.DB.First(&updated, account.ID).Error == nil && updated.Status == "error" && updated.LeaseUntil == 0
	}, 3*time.Second, 10*time.Millisecond)
	assert.Zero(t, calls.Load(), "worker credentials require a verified TLS peer")
}

func TestGPTAccountPausedRecoveryRetriesAreBounded(t *testing.T) {
	identity := setupGPTAccountTest(t)
	channel := model.Channel{Type: 57, Key: `{"access_token":"original","refresh_token":"private-refresh","account_id":"account"}`, Name: "Retry", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	account := model.GPTAccount{OwnerID: identity.UserID, Email: "owner@example.com", ChannelID: &channel.Id, ChannelKeyHash: model.GPTChannelKeyHash(channel.Key), SecretID: "retry", Generation: 1, AutoRelogin: true, Status: "error"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password"}))
	require.NoError(t, model.DB.Create(&account).Error)
	job, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	require.NoError(t, model.PauseGPTAccountChannel(job))
	service.ReleaseGPTAccountJob(job, "refresh_failed")
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"next_retry_at": 0, "failure_count": 0}).Error)
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	var refreshCalls, loginCalls, workerStatus atomic.Int32
	workerStatus.Store(http.StatusBadGateway)
	client.Transport = gptOAuthTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		refreshCalls.Add(1)
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loginCalls.Add(1)
		w.WriteHeader(int(workerStatus.Load()))
	}))
	defer worker.Close()
	tokenFile := filepath.Join(t.TempDir(), "worker-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600))
	t.Setenv("GPT_ACCOUNT_WORKER_URL", worker.URL)
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", tokenFile)
	for attempt := range 3 {
		service.ScheduleGPTAccountRecoveries()
		require.Eventually(t, func() bool {
			var updated model.GPTAccount
			status := "error"
			if attempt == 2 {
				status = "needs_attention"
			}
			return model.DB.First(&updated, account.ID).Error == nil && updated.Status == status && updated.LeaseUntil == 0 && updated.FailureCount == attempt+1
		}, 3*time.Second, 10*time.Millisecond)
		assert.EqualValues(t, attempt+1, refreshCalls.Load(), "each bounded retry refreshes before browser login")
		assert.EqualValues(t, attempt+1, loginCalls.Load())
		service.ScheduleGPTAccountRecoveries()
		assert.EqualValues(t, attempt+1, loginCalls.Load(), "persisted cooldown prevents immediate retry")
		require.NoError(t, model.DB.Model(&account).Update("next_retry_at", 0).Error)
	}
	service.ScheduleGPTAccountRecoveries()
	assert.EqualValues(t, 3, loginCalls.Load(), "three failures require operator intervention")
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, channel.Status)
	assert.Equal(t, model.GPTAccountRecoveryReason, channel.GetOtherInfo()["status_reason"])
	workerStatus.Store(http.StatusTooManyRequests)
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"status": "error", "next_retry_at": 0, "failure_count": 0}).Error)
	_, _, err = service.RefreshCodexChannelCredential(context.Background(), channel.Id, service.CodexCredentialRefreshOptions{})
	assert.Error(t, err, "proactive refresh must leave an owned paused channel to the recovery scheduler")
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Zero(t, account.NextRetryAt)
	assert.EqualValues(t, 3, refreshCalls.Load())
	service.ScheduleGPTAccountRecoveries()
	require.Eventually(t, func() bool {
		var updated model.GPTAccount
		return model.DB.First(&updated, account.ID).Error == nil && updated.Status == "error" && updated.LeaseUntil == 0 && updated.LastErrorCode == "worker_busy" && updated.FailureCount == 0 && updated.NextRetryAt > time.Now().Unix()
	}, 3*time.Second, 10*time.Millisecond, "worker contention must not exhaust a credential retry budget")
}

func TestGPTAccountProactiveRefreshPreservesRecoveryBudget(t *testing.T) {
	identity := setupGPTAccountTest(t)
	channel := model.Channel{Type: 57, Key: `{"access_token":"original","refresh_token":"private-refresh","account_id":"account"}`, Name: "Refresh budget", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	account := model.GPTAccount{OwnerID: identity.UserID, ChannelID: &channel.Id, ChannelKeyHash: model.GPTChannelKeyHash(channel.Key), SecretID: "budget", Generation: 1, AutoRelogin: true, Status: "ready"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{}))
	require.NoError(t, model.DB.Create(&account).Error)
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = gptOAuthTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}}
	for range 3 {
		_, _, err = service.RefreshCodexChannelCredential(context.Background(), channel.Id, service.CodexCredentialRefreshOptions{})
		assert.Error(t, err)
		require.NoError(t, model.DB.Model(&account).Update("next_retry_at", 0).Error)
	}
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Zero(t, account.FailureCount)
	assert.True(t, service.ScheduleGPTAccountRecovery(channel.Id, channel.Key))
	require.Eventually(t, func() bool {
		var updated model.GPTAccount
		return model.DB.First(&updated, account.ID).Error == nil && updated.Status == "needs_attention" && updated.LastErrorCode == "credentials_required" && updated.LeaseUntil == 0
	}, 3*time.Second, 10*time.Millisecond, "a real401 must reach browser recovery even after proactive refresh failures")
}

func TestGPTAccountSchemaFreshAndReleasedUpgrade(t *testing.T) {
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("released_upgrade_%v", upgrade), func(t *testing.T) {
			db, _ := newAuditTestDatabase(t, dialect, os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN"))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			channel := model.Channel{Type: 57, Name: "Existing released channel", Key: "unchanged-key", Group: "default", Models: "gpt-5", Status: common.ChannelStatusEnabled}
			if upgrade {
				// The persisted Channel, Ability and AuthFlow schemas are unchanged
				// from v1.0.0-rc.41 (verified with git diff for those model files).
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.AuthFlow{}))
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, channel.AddAbilities(db))
			}
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.AuthFlow{}, &model.GPTAccount{}))
			}
			if upgrade {
				var stored model.Channel
				require.NoError(t, db.First(&stored, channel.Id).Error)
				assert.Equal(t, channel.Key, stored.Key)
				var abilities []model.Ability
				require.NoError(t, db.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
				require.Len(t, abilities, 1)
				assert.Equal(t, "gpt-5", abilities[0].Model)
			}
			channelID := 123
			account := model.GPTAccount{OwnerID: 1, Name: "Preserved account", SecretID: "cipher-binding", SecretsCiphertext: "persisted-ciphertext", Generation: 7, ChannelID: &channelID}
			require.NoError(t, db.Create(&account).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.GPTAccount{}))
			}
			var stored model.GPTAccount
			require.NoError(t, db.First(&stored, account.ID).Error)
			assert.Equal(t, account.SecretsCiphertext, stored.SecretsCiphertext)
			assert.Equal(t, account.Generation, stored.Generation)
			assert.Equal(t, account.ChannelID, stored.ChannelID)
			assert.Error(t, db.Create(&model.GPTAccount{OwnerID: 2, SecretID: "duplicate", ChannelID: &channelID}).Error)
			for range 2 {
				require.NoError(t, db.Create(&model.GPTAccount{OwnerID: 1, SecretID: "unlinked"}).Error, "multiple unlinked accounts must remain valid")
			}
		})
	}
}

func TestGPTAccountAbandonedJobRequiresManualRetry(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "abandoned", Generation: 1, AutoRelogin: true, Status: "logging_in", LeaseID: "crashed-worker", LeaseUntil: time.Now().Unix() - 1}
	require.NoError(t, model.DB.Create(&account).Error)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", "", ListGPTAccounts)
	assert.Contains(t, listed.Body.String(), `"status":"needs_attention"`)
	assert.Contains(t, listed.Body.String(), `"last_error_code":"login_timeout"`)
	_, err := service.AcquireGPTAccountJob(account.ID, true)
	assert.Error(t, err, "an abandoned browser job cannot restart unattended")
	_, err = service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err, "manual retry can replace the expired lease")
}

func TestGPTAccountRefreshRespectsAttentionAndOwnerRevocation(t *testing.T) {
	identity := setupGPTAccountTest(t)
	channel := model.Channel{Type: 57, Key: `{"access_token":"original","refresh_token":"private-refresh","account_id":"account"}`, Name: "Refresh", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	account := model.GPTAccount{OwnerID: identity.UserID, ChannelID: &channel.Id, ChannelKeyHash: model.GPTChannelKeyHash(channel.Key), SecretID: "refresh", Generation: 1, AutoRelogin: true, Status: "needs_attention", LastErrorCode: "captcha_required"}
	require.NoError(t, model.DB.Create(&account).Error)
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previousTransport := client.Transport
	t.Cleanup(func() { client.Transport = previousTransport })
	calls := 0
	client.Transport = gptOAuthTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("test rejects unexpected refresh")
	}}
	_, _, err = service.RefreshCodexChannelCredential(context.Background(), channel.Id, service.CodexCredentialRefreshOptions{})
	assert.Error(t, err)
	assert.Zero(t, calls, "background channel refresh must preserve the attention requirement")
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, "needs_attention", account.Status)
	assert.Equal(t, "captcha_required", account.LastErrorCode)
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"status": "ready", "auto_relogin": false, "next_retry_at": 0, "failure_count": 0}).Error)
	claims, err := common.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account"}})
	require.NoError(t, err)
	access := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	client.Transport = gptOAuthTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		calls++
		body, _ := common.Marshal(map[string]any{"access_token": access, "refresh_token": "rotated", "expires_in": 3600})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}}
	_, _, err = service.RefreshCodexChannelCredential(context.Background(), channel.Id, service.CodexCredentialRefreshOptions{})
	require.NoError(t, err, "disabling browser relogin must still permit regular token refresh")
	assert.Equal(t, 1, calls)
	job, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("role", common.RoleCommonUser).Error)
	assert.ErrorIs(t, model.StoreGPTAccountCredential(job, "must-not-store", "account"), model.ErrGPTAccountStale)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.NotEqual(t, "must-not-store", channel.Key)
}

func setupGPTAccountTest(t *testing.T) service.AuthIdentity {
	t.Helper()
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.GPTAccount{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, db.AutoMigrate(&model.GPTAccount{}))
	previousMaster, previousMemory := common.IsMasterNode, common.MemoryCacheEnabled
	common.IsMasterNode, common.MemoryCacheEnabled = true, false
	t.Cleanup(func() { common.IsMasterNode, common.MemoryCacheEnabled = previousMaster, previousMemory })
	require.NoError(t, authz.Init(db))
	common.IsMasterNode = false
	path := filepath.Join(t.TempDir(), "encryption.key")
	require.NoError(t, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))), 0600))
	t.Setenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE", path)
	t.Setenv("GPT_ACCOUNT_WORKER_URL", "")
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", "")
	identity, _ := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(`{"user_id":1,"action":"enable"}`)})
	return identity
}

type gptOAuthTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (transport gptOAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.roundTrip(request)
}

func TestGPTAccountOAuthIdentityReplayAndChannelCreation(t *testing.T) {
	identity := setupGPTAccountTest(t)
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Managed","email":"owner@example.com","models":"gpt-5","group":"default"}`, "gpt_account.create", CreateGPTAccount)
	require.Contains(t, created.Body.String(), `"success":true`)
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	authorization, err := service.StartGPTAccountAuthorization(account.ID, identity)
	require.NoError(t, err)
	authURL, err := url.Parse(authorization.AuthorizationURL)
	require.NoError(t, err)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	claims := jwt.MapClaims{"iss": "https://auth.openai.com", "aud": "app_EMoamEEZ73f0CkXaXp7hrann", "sub": "user-1", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": authURL.Query().Get("nonce"), "email": "owner@example.com", "email_verified": true, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-1"}}
	idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	idToken.Header["kid"] = "test-key"
	signed, err := idToken.SignedString(privateKey)
	require.NoError(t, err)
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previousTransport := client.Transport
	t.Cleanup(func() { client.Transport = previousTransport })
	exchanges := 0
	client.Transport = gptOAuthTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "auth.openai.com", request.URL.Host)
		var payload any
		switch request.URL.Path {
		case "/oauth/token":
			exchanges++
			require.NoError(t, request.ParseForm())
			hash := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
			assert.Equal(t, authURL.Query().Get("code_challenge"), base64.RawURLEncoding.EncodeToString(hash[:]))
			payload = map[string]any{"id_token": signed, "access_token": "access-private", "refresh_token": "refresh-private", "expires_in": 3600}
		case "/.well-known/openid-configuration":
			payload = map[string]any{"issuer": "https://auth.openai.com", "authorization_endpoint": "https://auth.openai.com/oauth/authorize", "token_endpoint": "https://auth.openai.com/oauth/token", "jwks_uri": "https://auth.openai.com/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}}
		case "/jwks":
			payload = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes())}}}
		default:
			t.Fatalf("unexpected OAuth request path %s", request.URL.Path)
		}
		body, encodeErr := common.Marshal(payload)
		require.NoError(t, encodeErr)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}}
	callback := "http://localhost:1455/auth/callback?" + url.Values{"code": {"one-time-code"}, "state": {authURL.Query().Get("state")}}.Encode()
	require.NoError(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, identity, callback))
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	require.NotNil(t, account.ChannelID)
	assert.Equal(t, "ready", account.Status)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, *account.ChannelID).Error)
	assert.Equal(t, 57, channel.Type)
	assert.Equal(t, "Managed", channel.Name)
	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "gpt-5", abilities[0].Model)
	assert.Error(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, identity, callback))
	assert.Equal(t, 1, exchanges)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", "", ListGPTAccounts)
	assert.NotContains(t, listed.Body.String(), "access-private")
	assert.NotContains(t, listed.Body.String(), "refresh-private")
	assert.NotContains(t, listed.Body.String(), signed)
	originalKey := channel.Key
	for _, scenario := range []string{"email", "nonce", "unverified_email", "audience", "issuer", "expiry", "signature", "missing_subject", "changed_subject"} {
		t.Run(scenario, func(t *testing.T) {
			authorization, err = service.StartGPTAccountAuthorization(account.ID, identity)
			require.NoError(t, err)
			authURL, err = url.Parse(authorization.AuthorizationURL)
			require.NoError(t, err)
			claims = jwt.MapClaims{"iss": "https://auth.openai.com", "aud": "app_EMoamEEZ73f0CkXaXp7hrann", "sub": "user-1", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": authURL.Query().Get("nonce"), "email": "owner@example.com", "email_verified": true, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-1"}}
			switch scenario {
			case "email":
				claims["email"] = "different@example.com"
			case "nonce":
				claims["nonce"] = "wrong-nonce"
			case "unverified_email":
				claims["email_verified"] = false
			case "audience":
				claims["aud"] = "different-client"
			case "issuer":
				claims["iss"] = "https://attacker.invalid"
			case "expiry":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "missing_subject":
				delete(claims, "sub")
			case "changed_subject":
				claims["sub"] = "different-user"
			}
			idToken = jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			idToken.Header["kid"] = "test-key"
			signed, err = idToken.SignedString(privateKey)
			require.NoError(t, err)
			if scenario == "signature" {
				parts := strings.Split(signed, ".")
				parts[2] = "AAAA"
				signed = strings.Join(parts, ".")
			}
			callback = "http://localhost:1455/auth/callback?" + url.Values{"code": {"new-code"}, "state": {authURL.Query().Get("state")}}.Encode()
			assert.Error(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, identity, callback))
			require.NoError(t, model.DB.First(&channel, channel.Id).Error)
			assert.Equal(t, originalKey, channel.Key)
		})
	}
}

func TestGPTAccountJobFencingAndAutomaticCooldown(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Fenced", Email: "owner@example.com", Group: "default", Models: "gpt-5", SecretID: "seed", Generation: 1, AutoRelogin: true, Status: "ready"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{}))
	require.NoError(t, model.DB.Create(&account).Error)
	job, err := service.AcquireGPTAccountJob(account.ID, true)
	require.NoError(t, err)
	_, err = service.AcquireGPTAccountJob(account.ID, true)
	assert.Error(t, err, "one durable job at a time")
	service.ReleaseGPTAccountJob(job, "refresh_failed")
	_, err = service.AcquireGPTAccountJob(account.ID, true)
	assert.Error(t, err, "401 floods must respect persisted cooldown")
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"next_retry_at": 0, "failure_count": 3}).Error)
	_, err = service.AcquireGPTAccountJob(account.ID, true)
	assert.Error(t, err, "bounded failures stop retries")
	manual, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&account).Update("generation", 2).Error)
	assert.ErrorIs(t, model.StoreGPTAccountCredential(manual, `{"access_token":"never-stored"}`, "account"), model.ErrGPTAccountStale)
	var count int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&count).Error)
	assert.Zero(t, count)
	service.ReleaseGPTAccountJob(manual, "refresh_failed")
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"lease_id": "", "lease_until": 0, "generation": 2}).Error)
	channel := model.Channel{Type: 57, Key: `{"access_token":"original","account_id":"account"}`, Name: "Preserve manual changes", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	require.NoError(t, model.DB.Model(&account).Updates(map[string]any{"channel_id": channel.Id, "channel_key_hash": model.GPTChannelKeyHash(channel.Key)}).Error)
	manual, err = service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	require.NoError(t, model.PauseGPTAccountChannel(manual))
	require.NoError(t, model.DB.Model(&channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
	assert.ErrorIs(t, model.StoreGPTAccountCredential(manual, `{"access_token":"replacement"}`, "account"), model.ErrGPTAccountStale)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{"status": common.ChannelStatusEnabled, "key": "newer-manual-key"}).Error)
	assert.ErrorIs(t, model.StoreGPTAccountCredential(manual, `{"access_token":"replacement"}`, "account"), model.ErrGPTAccountStale)
	require.NoError(t, model.DB.Delete(&account).Error)
	assert.Error(t, model.StoreGPTAccountCredential(manual, `{"access_token":"replacement"}`, "account"))
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	assert.Equal(t, "newer-manual-key", channel.Key)
}

func TestGPTAccountOwnershipProofAndExpiredFlow(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID + 1, Name: "Other owner", Email: "owner@example.com", SecretID: "other", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{}))
	require.NoError(t, model.DB.Create(&account).Error)
	listed := gptAccountRequest(t, identity, common.RoleAdminUser, 0, http.MethodGet, "", "", ListGPTAccounts)
	assert.NotContains(t, listed.Body.String(), "Other owner")
	denied := gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodDelete, "", "gpt_account.delete", DeleteGPTAccount)
	assert.Equal(t, http.StatusNotFound, denied.Code)
	require.NoError(t, model.DB.Model(&account).Update("owner_id", identity.UserID).Error)
	authorization, err := service.StartGPTAccountAuthorization(account.ID, identity)
	require.NoError(t, err)
	u, err := url.Parse(authorization.AuthorizationURL)
	require.NoError(t, err)
	callback := "http://localhost:1455/auth/callback?" + url.Values{"code": {"unused"}, "state": {u.Query().Get("state")}}.Encode()
	otherIdentity := identity
	otherIdentity.SessionID = "other-session"
	assert.Error(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, otherIdentity, callback))
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", "gpt_account_oauth").Update("expires_at", time.Now().Add(-time.Minute)).Error)
	assert.Error(t, service.FinishGPTAccountAuthorization(context.Background(), account.ID, identity, callback))
	var channelCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channelCount).Error)
	assert.Zero(t, channelCount)
}

func gptAccountRequest(t *testing.T, identity service.AuthIdentity, role int, id int, method, body, scope string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	proof := ""
	if scope != "" {
		ctx := []byte(fmt.Sprintf(`{"account_id":%d}`, id))
		if scope == "gpt_account.create" {
			ctx = []byte(`{"email":"owner@example.com"}`)
		}
		proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: scope, Context: ctx}, service.VerificationMethodPassword)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/gpt-accounts", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Security-Proof", proof)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
	c.Set("id", identity.UserID)
	c.Set("role", role)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	handler(c)
	return recorder
}

func TestGPTAccountSecretsPermissionsAndDelete(t *testing.T) {
	identity := setupGPTAccountTest(t)
	body := `{"name":"Primary","email":"owner@example.com","password":"private-password","totp_secret":"JBSWY3DPEHPK3PXP","models":"gpt-5","group":"default"}`
	denied := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, "", CreateGPTAccount)
	assert.Equal(t, http.StatusForbidden, denied.Code)
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, "gpt_account.create", CreateGPTAccount)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Contains(t, created.Body.String(), `"success":true`)
	assert.NotContains(t, created.Body.String(), "private-password")
	assert.NotContains(t, created.Body.String(), "JBSWY3DPEHPK3PXP")
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	assert.True(t, account.AutoRelogin)
	assert.NotContains(t, account.SecretsCiphertext, "private-password")
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "private-password", secrets.Password)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
	listed := gptAccountRequest(t, identity, common.RoleCommonUser, 0, http.MethodGet, "", "", ListGPTAccounts)
	assert.Equal(t, http.StatusForbidden, listed.Code)
	updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, `{"name":"Updated","email":"owner@example.com","password":"","totp_secret":"","models":"gpt-5","group":"default","auto_relogin":false}`, "gpt_account.update", UpdateGPTAccount)
	require.Contains(t, updated.Body.String(), `"success":true`, updated.Body.String())
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.False(t, account.AutoRelogin)
	secrets, err = service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "private-password", secrets.Password)
	channel := model.Channel{Type: 57, Key: `{"access_token":"channel-secret","account_id":"acct"}`, Name: "Preserved", Status: 1}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Model(&account).Update("channel_id", channel.Id).Error)
	for _, changes := range []map[string]string{{"group": "different-group", "models": "gpt-5"}, {"group": "default", "models": "different-model"}} {
		changes["name"], changes["email"] = "Updated", "owner@example.com"
		encoded, err := common.Marshal(changes)
		require.NoError(t, err)
		response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, string(encoded), "gpt_account.update", UpdateGPTAccount)
		assert.Equal(t, http.StatusBadRequest, response.Code, "attached routing metadata must not silently diverge from its channel")
	}
	deleted := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodDelete, "", "gpt_account.delete", DeleteGPTAccount)
	require.Contains(t, deleted.Body.String(), `"success":true`)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-password")
	assert.NotContains(t, string(encoded), "JBSWY3DPEHPK3PXP")
}

func TestGPTAccountOAuthStateAndEncryptionFailClosed(t *testing.T) {
	identity := setupGPTAccountTest(t)
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Primary","email":"owner@example.com","models":"gpt-5","group":"default"}`, "gpt_account.create", CreateGPTAccount)
	require.Contains(t, created.Body.String(), `"success":true`, created.Body.String())
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	oauth := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodGet, "", "gpt_account.oauth", StartGPTAccountOAuth)
	require.Contains(t, oauth.Body.String(), `"success":true`, oauth.Body.String())
	var response struct {
		Data struct {
			AuthorizationURL string `json:"authorization_url"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(oauth.Body.Bytes(), &response))
	u, err := url.Parse(response.Data.AuthorizationURL)
	require.NoError(t, err)
	assert.Equal(t, "auth.openai.com", u.Host)
	assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	assert.NotEmpty(t, u.Query().Get("state"))
	assert.NotEmpty(t, u.Query().Get("code_challenge"))
	bad := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{"redirect_url":"http://localhost:1455/auth/callback?state=forged&code=code"}`, "", CompleteGPTAccountOAuth)
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	t.Setenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE", "")
	_, err = service.DecryptGPTAccountSecrets(&account)
	assert.Error(t, err)
	unavailable := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"No key","email":"owner@example.com"}`, "gpt_account.create", CreateGPTAccount)
	assert.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
}
