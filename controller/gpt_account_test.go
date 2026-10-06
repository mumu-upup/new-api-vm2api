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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
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
	"gorm.io/gorm"
)

func testCodexOAuthIDToken(email, accountID, subject string) string {
	return testCodexOAuthIDTokenWithExpiryAndIssuer(email, accountID, subject, time.Now().Add(time.Hour), "https://auth.openai.com")
}

func testCodexOAuthIDTokenWithIssuer(email, accountID, subject, issuer string) string {
	return testCodexOAuthIDTokenWithExpiryAndIssuer(email, accountID, subject, time.Now().Add(time.Hour), issuer)
}

func testCodexOAuthIDTokenWithExpiry(email, accountID, subject string, expiry time.Time) string {
	return testCodexOAuthIDTokenWithExpiryAndIssuer(email, accountID, subject, expiry, "https://auth.openai.com")
}

func testCodexOAuthIDTokenWithExpiryAndIssuer(email, accountID, subject string, expiry time.Time, issuer string) string {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": issuer, "aud": "app_EMoamEEZ73f0CkXaXp7hrann", "sub": subject,
		"exp": expiry.Unix(), "email": email, "email_verified": true,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	signed, err := token.SignedString(privateKey)
	if err != nil {
		panic(err)
	}
	return signed
}

func testCodexOAuthAccessToken(t *testing.T, email, accountID, subject string, expires int64, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": "https://auth.openai.com", "aud": []string{"https://api.openai.com/v1"}, "sub": subject,
		"exp": expires, "nbf": time.Now().Add(-time.Minute).Unix(),
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": accountID},
		"https://api.openai.com/profile": map[string]any{"email": email, "email_verified": true},
	}
	for key, value := range overrides {
		claims[key] = value
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
	require.NoError(t, err)
	return signed
}

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

func TestGPTAccountDefaultGroupConfigPersistsAndProtectsExistingRouting(t *testing.T) {
	identity := setupGPTAccountTest(t)
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("role", common.RoleRootUser).Error)
	pat, _ := createScopedAccessToken(t, identity.UserID, time.Now().Add(time.Hour).Unix(), "option:write")
	engine := gin.New()
	engine.PUT("/api/option/", middleware.RootAuth(), UpdateOption)
	engine.PUT("/api/gpt-accounts/config", middleware.RootAuth(), UpdateGPTAccountsConfig)
	for _, request := range []struct{ path, body string }{
		{"/api/option/", `{"key":"GPTAccountDefaultGroup","value":"forbidden"}`},
		{"/api/option/", `{"key":"gptaccountdefaultgroup","value":"forbidden"}`},
		{"/api/option/", `{"key":"GPTAccountDefaultGroup ","value":"forbidden"}`},
		{"/api/option/", `{"key":"GPTAccountDéfaultGroup","value":"forbidden"}`},
		{"/api/gpt-accounts/config", `{"default_group":"forbidden"}`},
	} {
		response := accessTokenRequest(engine, http.MethodPut, request.path, pat, "", request.body)
		assert.Contains(t, []int{http.StatusForbidden, http.StatusBadRequest}, response.Code, request.path+request.body)
	}
	response := accessTokenRequest(engine, http.MethodPut, "/api/option/", pat, "", `{"key":"Notice","value":"测试公告"}`)
	assert.Equal(t, http.StatusOK, response.Code)
	config := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", GPTAccountsConfig)
	assert.Contains(t, config.Body.String(), `"default_group":"default"`)
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		denied := gptAccountRequest(t, identity, role, 0, http.MethodPut, `{"default_group":"forbidden"}`, UpdateGPTAccountsConfig)
		assert.Equal(t, http.StatusForbidden, denied.Code)
	}
	for _, value := range []string{"", " ", "team\nother", strings.Repeat("x", 65), "team,,other", "team\x00"} {
		body, err := common.Marshal(map[string]string{"default_group": value})
		require.NoError(t, err)
		response := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPut, string(body), UpdateGPTAccountsConfig)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
	saved := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPut, `{"default_group":" team "}`, UpdateGPTAccountsConfig)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	var option model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: "GPTAccountDefaultGroup"}).First(&option).Error)
	assert.Equal(t, "team", option.Value)
	raw := fmt.Sprintf(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":%q,"account_id":"existing","email":"existing@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("existing@example.com", "existing", "existing"))
	account, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, service.GPTAccountOAuthImportInput{OAuthJSON: raw})
	require.NoError(t, err)
	assert.Equal(t, "team", account.Group)
	saved = gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPut, `{"default_group":"next"}`, UpdateGPTAccountsConfig)
	require.Equal(t, http.StatusOK, saved.Code)
	config = gptAccountRequest(t, identity, common.RoleAdminUser, 0, http.MethodGet, "", GPTAccountsConfig)
	assert.Contains(t, config.Body.String(), `"default_group":"next"`)
	account, err = service.ImportGPTAccountOAuth(context.Background(), identity.UserID, account.ID, service.GPTAccountOAuthImportInput{OAuthJSON: raw})
	require.NoError(t, err)
	assert.Equal(t, "team", account.Group)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, *account.ChannelID).Error)
	assert.Equal(t, "team", channel.Group)
	// A direct database read sees another node's update without cache synchronization.
	require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: "GPTAccountDefaultGroup"}).Update("value", "another-node").Error)
	group, err := model.ResolveGPTAccountGroup("")
	require.NoError(t, err)
	assert.Equal(t, "another-node", group)
	for _, explicit := range []string{"", "custom"} {
		input, err := service.ParseGPTGatewayChannelJSON(fmt.Sprintf(`{"provider":"cpa","base_url":"https://cpa.example","api_key":"fixture","group":%q}`, explicit))
		require.NoError(t, err)
		channel, err := service.CreateGPTGatewayChannel(input)
		require.NoError(t, err)
		want := explicit
		if want == "" {
			want = "another-node"
		}
		assert.Equal(t, want, channel.Group)
	}
	// Invalid option writes through the shared settings API also fail validation.
	require.Error(t, model.UpdateOptionsBulk(map[string]string{model.GPTAccountDefaultGroupKey: ""}))
}

func TestGPTAccountDefaultGroupAppliesToNewAccountsAndImports(t *testing.T) {
	identity := setupGPTAccountTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	require.NoError(t, model.DB.Create(&model.Option{Key: "GPTAccountDefaultGroup", Value: "team"}).Error)
	config := gptAccountRequest(t, identity, common.RoleAdminUser, 0, http.MethodGet, "", GPTAccountsConfig)
	assert.Contains(t, config.Body.String(), `"default_group":"team"`)
	for _, tc := range []struct{ name, group, want string }{
		{"inherit", "", "team"}, {"override", "premium", "premium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			body, err := common.Marshal(map[string]string{"name": "Account", "email": "owner@example.com", "group": tc.group})
			require.NoError(t, err)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/gpt-accounts", strings.NewReader(string(body)))
			c.Request.Header.Set("Content-Type", "application/json")
			input, ok := normalizeGPTAccountInput(c)
			require.True(t, ok)
			assert.Equal(t, tc.want, input.Group)
			email := tc.name + "@example.com"
			raw := fmt.Sprintf(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":%q,"account_id":%q,"email":%q,"expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken(email, tc.name, tc.name), tc.name, email)
			account, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, service.GPTAccountOAuthImportInput{OAuthJSON: raw, Group: tc.group})
			require.NoError(t, err)
			assert.Equal(t, tc.want, account.Group)
			require.NotNil(t, account.ChannelID)
			var channel model.Channel
			require.NoError(t, model.DB.First(&channel, *account.ChannelID).Error)
			assert.Equal(t, tc.want, channel.Group)
			var abilities []model.Ability
			require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
			require.NotEmpty(t, abilities)
			assert.Equal(t, tc.want, abilities[0].Group)
		})
	}
}

func TestGPTAccountOAuthImportValidation(t *testing.T) {
	raw := fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"account_id":"acct-1","email":"Owner@Example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("owner@example.com", "acct-1", "subject-1"))
	key, err := service.ParseCodexOAuthImport(raw)
	require.NoError(t, err)
	assert.Equal(t, "owner@example.com", key.Email)
	assert.Equal(t, "codex", key.Type)
	for _, invalid := range []string{
		fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"account_id":"acct-1","email":"owner@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDTokenWithIssuer("owner@example.com", "acct-1", "subject-1", "https://other.example")),
		fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"account_id":"acct-1","email":"owner@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDTokenWithExpiry("owner@example.com", "acct-1", "subject-1", time.Unix(0, 0))),
	} {
		_, err := service.ParseCodexOAuthImport(invalid)
		assert.Error(t, err)
	}

	for _, invalid := range []string{
		`{"access_token":"","refresh_token":"refresh","id_token":"e30.eyJzdWIiOiJzdWJqZWN0LTEifQ.sig","account_id":"acct-1","email":"owner@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`,
		`{"access_token":"access","refresh_token":"refresh","id_token":"e30.eyJzdWIiOiJzdWJqZWN0LTEifQ.sig","account_id":"acct-1","email":"owner@example.com","expired":"2000-01-02T03:04:05Z","type":"codex"}`,
		`{"access_token":"access","refresh_token":"refresh","id_token":"e30.eyJzdWIiOiJzdWJqZWN0LTEifQ.sig","account_id":"acct-1","email":"owner@example.com","expired":"2099-01-02T03:04:05Z","type":"other"}`,
		`{"access_token":"access","refresh_token":"refresh","id_token":"not-a-jwt","account_id":"acct-1","email":"owner@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`,
		strings.Repeat("x", 32<<10),
	} {
		_, err := service.ParseCodexOAuthImport(invalid)
		assert.Error(t, err)
	}
}

func TestGPTAccountOAuthImportUsesAccessTokenLifetimeAndIdentity(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour).Unix()
	for _, tc := range []struct {
		name      string
		overrides jwt.MapClaims
		valid     bool
	}{
		{name: "current access with historical ID metadata", valid: true},
		{name: "expired access", overrides: jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}},
		{name: "missing access expiry", overrides: jwt.MapClaims{"exp": nil}},
		{name: "invalid access expiry", overrides: jwt.MapClaims{"exp": "later"}},
		{name: "fractional access expiry", overrides: jwt.MapClaims{"exp": float64(expires) + 0.5}},
		{name: "wrong issuer", overrides: jwt.MapClaims{"iss": "https://other.example"}},
		{name: "wrong audience", overrides: jwt.MapClaims{"aud": "other-api"}},
		{name: "future not before", overrides: jwt.MapClaims{"nbf": expires}},
		{name: "invalid not before", overrides: jwt.MapClaims{"nbf": "later"}},
		{name: "different subject", overrides: jwt.MapClaims{"sub": "another-subject"}},
		{name: "different account", overrides: jwt.MapClaims{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "another-account"}}},
		{name: "different email", overrides: jwt.MapClaims{"https://api.openai.com/profile": map[string]any{"email": "another@example.com", "email_verified": true}}},
		{name: "missing email", overrides: jwt.MapClaims{"https://api.openai.com/profile": nil}},
		{name: "conflicting root email", overrides: jwt.MapClaims{"email": "another@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access := testCodexOAuthAccessToken(t, "owner@example.com", "acct-1", "subject-1", expires, tc.overrides)
			idExpiry := time.Now().Add(time.Hour)
			if tc.valid {
				idExpiry = time.Now().Add(-2 * time.Hour)
			}
			idToken := testCodexOAuthIDTokenWithExpiry("owner@example.com", "acct-1", "subject-1", idExpiry)
			raw, err := common.Marshal(service.CodexOAuthKey{AccessToken: access, RefreshToken: "fixture-refresh", IDToken: idToken, AccountID: "acct-1", Email: "owner@example.com", Expired: "2099-01-02T03:04:05Z", Type: "codex"})
			require.NoError(t, err)
			key, err := service.ParseCodexOAuthImport(string(raw))
			if !tc.valid {
				require.Error(t, err)
				assert.Equal(t, "oauth_import_invalid", err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, time.Unix(expires, 0).Format(time.RFC3339), key.Expired, "bundle metadata cannot extend the access JWT expiry")
			assert.Equal(t, idToken, key.IDToken, "historical metadata is retained without claiming authentication")
		})
	}
}

func TestGPTAccountExpiredOAuthLeaseIsReclaimedPersistently(t *testing.T) {
	setupGPTAccountTest(t)
	stale := model.GPTAccount{OwnerID: 1, SecretID: "expired-lease", Generation: 1, Status: "logging_in", LeaseID: "stale", LeaseUntil: time.Now().Unix() - 1, NextRetryAt: time.Now().Unix() + 300}
	active := model.GPTAccount{OwnerID: 1, SecretID: "active-lease", Generation: 1, Status: "logging_in", LeaseID: "active", LeaseUntil: time.Now().Unix() + 300}
	require.NoError(t, model.DB.Create(&stale).Error)
	require.NoError(t, model.DB.Create(&active).Error)

	changed, err := service.ReclaimExpiredGPTAccountJobs(time.Now().Unix())
	require.NoError(t, err)
	assert.EqualValues(t, 1, changed)
	require.NoError(t, model.DB.First(&stale, stale.ID).Error)
	assert.Equal(t, "needs_attention", stale.Status)
	assert.Equal(t, "login_timeout", stale.LastErrorCode)
	assert.Empty(t, stale.LeaseID)
	assert.Zero(t, stale.LeaseUntil)
	assert.Zero(t, stale.NextRetryAt)
	require.NoError(t, model.DB.First(&active, active.ID).Error)
	assert.Equal(t, "logging_in", active.Status)
	assert.Equal(t, "active", active.LeaseID)
	zeroLease := model.GPTAccount{OwnerID: 1, SecretID: "zero-lease", Generation: 1, Status: "logging_in"}
	require.NoError(t, model.DB.Create(&zeroLease).Error)
	changed, err = service.ReclaimExpiredGPTAccountJobs(time.Now().Unix())
	require.NoError(t, err)
	assert.EqualValues(t, 1, changed)
	require.NoError(t, model.DB.First(&zeroLease, zeroLease.ID).Error)
	assert.Equal(t, "needs_attention", zeroLease.Status)
	assert.Equal(t, "login_timeout", zeroLease.LastErrorCode)
}

func TestGPTAccountOAuthImportCreatesAndUpdatesOneCodexChannel(t *testing.T) {
	identity := setupGPTAccountTest(t)
	access := testCodexOAuthAccessToken(t, "import@example.com", "acct-import", "subject-import", time.Now().Add(time.Hour).Unix(), nil)
	raw := fmt.Sprintf(`{"access_token":%q,"refresh_token":"refresh-private","id_token":%q,"account_id":"acct-import","email":"import@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, access, testCodexOAuthIDTokenWithExpiry("import@example.com", "acct-import", "subject-import", time.Now().Add(-time.Hour)))
	input := service.GPTAccountOAuthImportInput{OAuthJSON: raw, Name: "Imported", Group: "default", Models: "gpt-5"}

	account, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, input)
	require.NoError(t, err)
	require.NotNil(t, account)
	assert.Equal(t, "import@example.com", account.Email)
	assert.Equal(t, "ready", account.Status)
	assert.True(t, account.AutoRelogin)
	require.NotNil(t, account.ChannelID)

	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, *account.ChannelID).Error)
	assert.Equal(t, 57, channel.Type)
	assert.Contains(t, channel.Key, access)

	var before int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&before).Error)
	updated, err := service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, input)
	require.NoError(t, err)
	assert.Equal(t, account.ID, updated.ID)
	var after int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

func TestGPTAccountOAuthImportConcurrentIdempotent(t *testing.T) {
	identity := setupGPTAccountTest(t)
	raw := fmt.Sprintf(`{"access_token":"concurrent-access","refresh_token":"concurrent-refresh","id_token":%q,"account_id":"acct-concurrent","email":"concurrent@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("concurrent@example.com", "acct-concurrent", "subject-concurrent"))
	input := service.GPTAccountOAuthImportInput{OAuthJSON: raw, Name: "Concurrent", Group: "default", Models: "gpt-5"}
	results := make([]*model.GPTAccount, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = service.ImportGPTAccountOAuth(context.Background(), identity.UserID, 0, input)
		}(i)
	}
	group.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID)
	var accounts, channels int64
	require.NoError(t, model.DB.Model(&model.GPTAccount{}).Count(&accounts).Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channels).Error)
	assert.EqualValues(t, 1, accounts)
	assert.EqualValues(t, 1, channels)
}

func TestGPTAccountOAuthImportControllerUsesAdminSession(t *testing.T) {
	identity := setupGPTAccountTest(t)
	raw := fmt.Sprintf(`{"access_token":"controller-access","refresh_token":"controller-refresh","id_token":%q,"account_id":"acct-controller","email":"controller@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("controller@example.com", "acct-controller", "subject-controller"))
	body := fmt.Sprintf(`{"oauth_json":%s,"name":"Controller import","group":"default","models":"gpt-5"}`, strconv.Quote(raw))
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, ImportGPTAccountOAuth)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.NotContains(t, created.Body.String(), "controller-access")
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, body, ImportGPTAccountOAuthForAccount)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
}

func TestGPTAccountOAuthImportRootUpdatesChildOwnerAccount(t *testing.T) {
	identity := setupGPTAccountTest(t)
	ownerID := identity.UserID + 1
	require.NoError(t, model.DB.Create(&model.User{Id: ownerID, Username: "child-owner", Password: "password-password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}).Error)
	account := model.GPTAccount{OwnerID: ownerID, Name: "Child account", Email: "child@example.com", SecretID: "child-owner-account", Generation: 1, Status: "pending"}
	require.NoError(t, model.DB.Create(&account).Error)
	raw := fmt.Sprintf(`{"oauth_json":%q,"name":"Child account","group":"default","models":"gpt-5"}`, fmt.Sprintf(`{"access_token":"child-access","refresh_token":"child-refresh","id_token":%q,"account_id":"child-acct","email":"child@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}`, testCodexOAuthIDToken("child@example.com", "child-acct", "child-subject")))
	response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, raw, ImportGPTAccountOAuthForAccount)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated model.GPTAccount
	require.NoError(t, model.DB.First(&updated, account.ID).Error)
	assert.Equal(t, ownerID, updated.OwnerID)
	assert.Equal(t, "ready", updated.Status)
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

func TestGPTAccountCredentialTamperingAndSessionAccess(t *testing.T) {
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
	ordinaryUser := gptAccountRequest(t, identity, common.RoleCommonUser, account.ID, http.MethodPut, body, UpdateGPTAccount)
	assert.Equal(t, http.StatusForbidden, ordinaryUser.Code)
	wrongAccount := gptAccountRequest(t, identity, common.RoleRootUser, account.ID+1, http.MethodPut, body, UpdateGPTAccount)
	assert.Equal(t, http.StatusNotFound, wrongAccount.Code)
	missingSession := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, body, func(c *gin.Context) {
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
		response := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, string(body), CreateGPTAccount)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	}
}

func TestGPTAccountTOTPProviderIsExplicitAndPreserved(t *testing.T) {
	identity := setupGPTAccountTest(t)
	for _, provider := range []string{"https://attacker.invalid", "2fa_CN", ""} {
		body := fmt.Sprintf(`{"name":"Primary","email":"owner@example.com","totp_provider":%q}`, provider)
		response := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, CreateGPTAccount)
		assert.Equal(t, http.StatusBadRequest, response.Code, "reject an explicitly invalid provider")
	}
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Primary","email":"owner@example.com","password":"private-password","totp_secret":"JBSWY3DPEHPK3PXP","totp_provider":"2fa_cn"}`, CreateGPTAccount)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"totp_provider":"2fa_cn"`)
	assert.NotContains(t, created.Body.String(), "JBSWY3DPEHPK3PXP")
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	for _, input := range []string{
		`{"name":"Primary","email":"owner@example.com"}`,
		`{"name":"Primary","email":"owner@example.com","totp_provider":"local"}`,
	} {
		updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, input, UpdateGPTAccount)
		require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
		provider := "2fa_cn"
		if strings.Contains(input, "local") {
			provider = "local"
		}
		assert.Contains(t, updated.Body.String(), fmt.Sprintf(`"totp_provider":%q`, provider))
		listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
		assert.Contains(t, listed.Body.String(), fmt.Sprintf(`"totp_provider":%q`, provider))
		require.NoError(t, model.DB.First(&account, account.ID).Error)
		secrets, err := service.DecryptGPTAccountSecrets(&account)
		require.NoError(t, err)
		assert.Equal(t, "private-password", secrets.Password)
		assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
		assert.Equal(t, provider, secrets.TOTPProvider)
	}
	legacy := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Legacy","email":"owner@example.com"}`, CreateGPTAccount)
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	assert.Contains(t, legacy.Body.String(), `"totp_provider":"local"`, "old clients must not opt into sending seeds to a third party")
}

func TestGPTAccountWorkerReceivesTOTPSourceAndReturnsSpecificError(t *testing.T) {
	identity := setupGPTAccountTest(t)
	received := make(chan map[string]string, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if common.DecodeJson(r.Body, &body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- body
		_, _ = io.WriteString(w, `{"status":"failed","error_code":"totp_provider_timeout"}`)
	}))
	defer worker.Close()
	tokenFile := filepath.Join(t.TempDir(), "worker-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600))
	t.Setenv("GPT_ACCOUNT_WORKER_URL", worker.URL)
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", tokenFile)
	for _, provider := range []string{"", "2fa_cn"} {
		account := model.GPTAccount{OwnerID: identity.UserID, Email: "owner@example.com", AccountID: "account-id-" + provider, SecretID: "worker-source-" + provider, Generation: 1}
		require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "fixture-password", TOTPSecret: "JBSWY3DPEHPK3PXP", TOTPProvider: provider}))
		require.NoError(t, model.DB.Create(&account).Error)
		require.NoError(t, service.StartGPTAccountLogin(account.ID, identity, false))
		var body map[string]string
		select {
		case body = <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not receive the login request")
		}
		expected := provider
		if expected == "" {
			expected = "local"
		}
		assert.Equal(t, expected, body["totp_provider"])
		assert.Equal(t, "fixture-password", body["password"])
		assert.Equal(t, "JBSWY3DPEHPK3PXP", body["totp_secret"])
		assert.Equal(t, account.AccountID, body["expected_account_id"])
		require.Eventually(t, func() bool {
			return model.DB.First(&account, account.ID).Error == nil && account.LeaseUntil == 0
		}, 3*time.Second, 10*time.Millisecond)
		assert.Equal(t, "error", account.Status)
		assert.Equal(t, "totp_provider_timeout", account.LastErrorCode)
	}
}

func TestGPTAccountGatewayProofScopes(t *testing.T) {
	identity := setupGPTAccountTest(t)
	for _, operation := range []service.VerificationOperation{
		{Scope: "gpt_account_gateway.create", Context: []byte(`{"name":"Example"}`)},
		{Scope: "gpt_account_gateway.update", Context: []byte(`{"gateway_id":1}`)},
		{Scope: "gpt_account.gateway.sync", Context: []byte(`{"account_id":1}`)},
	} {
		binding, err := service.BindVerificationOperation(operation)
		require.NoError(t, err, operation.Scope)
		assert.Equal(t, operation.Scope, binding.Scope)
		_, err = service.GetVerificationRequirements(identity, operation.Scope)
		require.NoError(t, err, operation.Scope)
	}
	for _, operation := range []service.VerificationOperation{
		{Scope: "gpt_account_gateway.create", Context: []byte(`{"name":""}`)},
		{Scope: "gpt_account_gateway.update", Context: []byte(`{"gateway_id":0}`)},
		{Scope: "gpt_account.gateway.sync", Context: []byte(`{"account_id":0}`)},
		{Scope: "gpt_account_gateway.update", Context: []byte(`{"gateway_id":1,"extra":"unbound"}`)},
	} {
		_, err := service.BindVerificationOperation(operation)
		assert.Error(t, err)
	}
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", identity.UserID).Update("role", common.RoleAdminUser).Error)
	for _, scope := range []string{"gpt_account_gateway.create", "gpt_account_gateway.update"} {
		_, err := service.GetVerificationRequirements(identity, scope)
		assert.ErrorIs(t, err, service.ErrVerificationForbidden)
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

// gptAccountBeforeTOTP preserves the persisted account schema from 955014a4d.
type gptAccountBeforeTOTP struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	OwnerID           int    `json:"-" gorm:"not null;index"`
	OwnerRole         int    `json:"-" gorm:"-"`
	Name              string `json:"name" gorm:"type:varchar(128);not null"`
	Email             string `json:"email" gorm:"type:varchar(254);not null;index"`
	ChannelID         *int   `json:"channel_id" gorm:"uniqueIndex"`
	ChannelName       string `json:"channel_name" gorm:"-"`
	Group             string `json:"group" gorm:"column:account_group;type:varchar(64)"`
	Models            string `json:"models" gorm:"type:text"`
	AutoRelogin       bool   `json:"auto_relogin"`
	Status            string `json:"status" gorm:"type:varchar(32)"`
	LastErrorCode     string `json:"last_error_code" gorm:"type:varchar(64)"`
	LastLoginAt       int64  `json:"last_login_at"`
	LastAttemptAt     int64  `json:"last_attempt_at"`
	NextRetryAt       int64  `json:"next_retry_at"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime"`
	HasPassword       bool   `json:"has_password"`
	HasTOTP           bool   `json:"has_totp"`
	CanManage         bool   `json:"can_manage" gorm:"-"`
	SecretID          string `json:"-" gorm:"type:varchar(64);not null"`
	SecretsCiphertext string `json:"-" gorm:"type:text;not null"`
	AccountID         string `json:"-" gorm:"type:varchar(128)"`
	OAuthSubject      string `json:"-" gorm:"type:varchar(255)"`
	ChannelKeyHash    string `json:"-" gorm:"type:char(64)"`
	Generation        int64  `json:"-" gorm:"not null"`
	LeaseID           string `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil        int64  `json:"-"`
	FailureCount      int    `json:"-"`
}

func (gptAccountBeforeTOTP) TableName() string { return "gpt_accounts" }

func TestGPTAccountSchemaFreshAndReleasedUpgrade(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "encryption.key")
	require.NoError(t, os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))), 0600))
	t.Setenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE", keyPath)
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
			legacy := model.GPTAccount{OwnerID: 1, Name: "Legacy account", Email: "legacy@example.com", SecretID: "legacy-binding", Generation: 7, AccountID: "legacy-subject", OAuthSubject: "oauth-subject"}
			require.NoError(t, service.EncryptGPTAccountSecrets(&legacy, service.GPTAccountSecrets{Password: "legacy-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
			if upgrade {
				// The persisted Channel, Ability and AuthFlow schemas are unchanged
				// from v1.0.0-rc.41 (verified with git diff for those model files).
				require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.AuthFlow{}))
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, channel.AddAbilities(db))
				require.NoError(t, db.AutoMigrate(&gptAccountBeforeTOTP{}))
				legacy.ChannelID = &channel.Id
				legacy.ChannelKeyHash = model.GPTChannelKeyHash(channel.Key)
				require.NoError(t, db.Omit("totp_version", "totp_updated_at", "totp_status", "gateway_id", "gateway_sync_status", "gateway_error_code", "gateway_synced_at").Create(&legacy).Error)
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
				var migrated model.GPTAccount
				require.NoError(t, db.First(&migrated, legacy.ID).Error)
				assert.Equal(t, legacy, migrated)
				secrets, err := service.DecryptGPTAccountSecrets(&migrated)
				require.NoError(t, err)
				assert.Equal(t, "legacy-password", secrets.Password)
				assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
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

func TestGPTAccountDeletedChannelIsNotReportedReady(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("memory_cache_%t", cacheEnabled), func(t *testing.T) {
			identity := setupGPTAccountTest(t)
			channel := model.Channel{Type: 57, Name: "Removed channel", Key: "fixture-key", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
			require.NoError(t, model.DB.Create(&channel).Error)
			require.NoError(t, channel.AddAbilities(nil))
			account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", SecretID: "removed-channel", Generation: 1, ChannelID: &channel.Id, Status: "ready"}
			require.NoError(t, model.DB.Create(&account).Error)
			if cacheEnabled {
				common.MemoryCacheEnabled = true
				model.InitChannelCache()
			}
			require.NoError(t, channel.Delete())
			listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
			require.Equal(t, http.StatusOK, listed.Code)
			var list struct {
				Data struct {
					Items []model.GPTAccount `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(listed.Body.Bytes(), &list))
			require.Len(t, list.Data.Items, 1)
			assert.Equal(t, "needs_attention", list.Data.Items[0].Status)
			assert.Equal(t, "channel_missing", list.Data.Items[0].LastErrorCode)
			assert.Empty(t, list.Data.Items[0].ChannelName)
			assert.Equal(t, account.ChannelID, list.Data.Items[0].ChannelID)
			response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodGet, "", func(c *gin.Context) {
				gptAccountResponse(c, account.ID)
			})
			require.Equal(t, http.StatusOK, response.Code)
			var detail struct {
				Data model.GPTAccount `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &detail))
			assert.Equal(t, "needs_attention", detail.Data.Status)
			assert.Equal(t, "channel_missing", detail.Data.LastErrorCode)
			var stored model.GPTAccount
			require.NoError(t, model.DB.First(&stored, account.ID).Error)
			assert.Equal(t, "ready", stored.Status, "reading metadata must not rewrite account state")
			assert.Equal(t, account.ChannelID, stored.ChannelID)
			var channelCount int64
			require.NoError(t, model.DB.Model(&model.Channel{}).Count(&channelCount).Error)
			assert.Zero(t, channelCount, "a deleted channel must not be recreated by a read")
		})
	}
}

func TestGPTAccountAbandonedJobRequiresManualRetry(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "abandoned", Generation: 1, AutoRelogin: true, Status: "logging_in", LeaseID: "crashed-worker", LeaseUntil: time.Now().Unix() - 1}
	require.NoError(t, model.DB.Create(&account).Error)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
	assert.Contains(t, listed.Body.String(), `"status":"needs_attention"`)
	assert.Contains(t, listed.Body.String(), `"last_error_code":"login_timeout"`)
	_, err := service.AcquireGPTAccountJob(account.ID, true)
	assert.Error(t, err, "an abandoned browser job cannot restart unattended")
	_, err = service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err, "manual retry can replace the expired lease")
}

func TestGPTAccountAutomaticLeasePreservesRetryCooldown(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "lease-duration", Generation: 1, Status: "pending", AutoRelogin: true}
	require.NoError(t, model.DB.Create(&account).Error)
	job, err := service.AcquireGPTAccountJob(account.ID, true)
	require.NoError(t, err)
	assert.Equal(t, int64(240), job.LeaseUntil-job.LastAttemptAt)
	assert.Equal(t, int64(900), job.NextRetryAt-job.LastAttemptAt)
}

func TestGPTAccountBrowserAuthorizationUsesItsAdvertisedLifetime(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "browser-duration", Generation: 1, Status: "pending"}
	require.NoError(t, model.DB.Create(&account).Error)
	authorization, err := service.StartGPTAccountAuthorization(account.ID, identity)
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, int64(600), account.LeaseUntil-account.LastAttemptAt)
	assert.Equal(t, authorization.ExpiresAt, account.LeaseUntil)
	stale := account
	stale.OwnerRole = common.RoleRootUser
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", "gpt_account_oauth").First(&flow).Error)
	assert.Equal(t, authorization.ExpiresAt, flow.ExpiresAt.Unix())
	changed, err := service.ReclaimExpiredGPTAccountJobs(authorization.ExpiresAt - 1)
	require.NoError(t, err)
	assert.Zero(t, changed, "the advertised browser authorization window must remain usable")
	changed, err = service.ReclaimExpiredGPTAccountJobs(authorization.ExpiresAt)
	require.NoError(t, err)
	assert.EqualValues(t, 1, changed)
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, "needs_attention", account.Status)
	assert.Equal(t, "login_timeout", account.LastErrorCode)
	fresh, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	service.ReleaseGPTAccountJob(&stale, "authorization_failed")
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, fresh.LeaseID, account.LeaseID, "late completion of an expired job must not release the new job")
	assert.Equal(t, fresh.LeaseUntil, account.LeaseUntil)
	assert.Equal(t, "logging_in", account.Status)
}

func TestGPTAccountWorkerDeadlinePrecedesItsLease(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Email: "owner@example.com", SecretID: "worker-deadline", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "fixture-password"}))
	require.NoError(t, model.DB.Create(&account).Error)
	leases := make(chan int64, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			State string `json:"state"`
		}
		if common.DecodeJson(r.Body, &input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var active model.GPTAccount
		if model.DB.First(&active, account.ID).Error != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		leases <- active.LeaseUntil
		body, err := common.Marshal(map[string]string{"status": "success", "state": input.State, "code": "fixture-code"})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer worker.Close()
	tokenFile := filepath.Join(t.TempDir(), "worker-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600))
	t.Setenv("GPT_ACCOUNT_WORKER_URL", worker.URL)
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", tokenFile)
	client, err := service.GetHttpClientWithProxy("")
	require.NoError(t, err)
	previousTransport := client.Transport
	t.Cleanup(func() { client.Transport = previousTransport })
	deadlines := make(chan time.Time, 1)
	client.Transport = gptOAuthTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
		deadline, _ := request.Context().Deadline()
		deadlines <- deadline
		return nil, errors.New("fixture exchange failure")
	}}
	require.NoError(t, service.StartGPTAccountLogin(account.ID, identity, false))
	var deadline time.Time
	select {
	case deadline = <-deadlines:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reach the OAuth exchange")
	}
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", "gpt_account_oauth").First(&flow).Error)
	leaseUntil := <-leases
	assert.Equal(t, leaseUntil, flow.ExpiresAt.Unix(), "automated authorization cannot outlive its worker lease")
	assert.GreaterOrEqual(t, time.Unix(leaseUntil, 0).Sub(deadline), 15*time.Second, "the worker must stop before the lease can be reclaimed")
	require.Eventually(t, func() bool {
		return model.DB.First(&account, account.ID).Error == nil && account.LeaseUntil == 0
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, "authorization_failed", account.LastErrorCode)
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
	require.NoError(t, db.AutoMigrate(&model.GPTAccount{}, &model.Channel{}, &model.Ability{}, &model.Option{}))
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
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Managed","email":"owner@example.com","models":"gpt-5","group":"default"}`, CreateGPTAccount)
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
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
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
	listed := gptAccountRequest(t, identity, common.RoleAdminUser, 0, http.MethodGet, "", ListGPTAccounts)
	assert.NotContains(t, listed.Body.String(), "Other owner")
	denied := gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodDelete, "", DeleteGPTAccount)
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

func gptAccountRequest(t *testing.T, identity service.AuthIdentity, role int, id int, method, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/gpt-accounts", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
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
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, body, CreateGPTAccount)
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
	listed := gptAccountRequest(t, identity, common.RoleCommonUser, 0, http.MethodGet, "", ListGPTAccounts)
	assert.Equal(t, http.StatusForbidden, listed.Code)
	updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, `{"name":"Updated","email":"owner@example.com","password":"","totp_secret":"","models":"gpt-5","group":"default","auto_relogin":false}`, UpdateGPTAccount)
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
		response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, string(encoded), UpdateGPTAccount)
		assert.Equal(t, http.StatusBadRequest, response.Code, "attached routing metadata must not silently diverge from its channel")
	}
	deleted := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodDelete, "", DeleteGPTAccount)
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
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Primary","email":"owner@example.com","models":"gpt-5","group":"default"}`, CreateGPTAccount)
	require.Contains(t, created.Body.String(), `"success":true`, created.Body.String())
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account).Error)
	oauth := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodGet, "", StartGPTAccountOAuth)
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
	bad := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{"redirect_url":"http://localhost:1455/auth/callback?state=forged&code=code"}`, CompleteGPTAccountOAuth)
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	t.Setenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE", "")
	_, err = service.DecryptGPTAccountSecrets(&account)
	assert.Error(t, err)
	unavailable := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"No key","email":"owner@example.com"}`, CreateGPTAccount)
	assert.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
}

func TestGPTAccountTOTPMetadataForImportedSecrets(t *testing.T) {
	identity := setupGPTAccountTest(t)
	created := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodPost, `{"name":"Primary","email":"owner@example.com","password":"private-password","totp_secret":"jbsw y3dp ehpk 3pxp"}`, CreateGPTAccount)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var response struct {
		Data struct {
			ID        int    `json:"id"`
			Version   int64  `json:"totp_version"`
			UpdatedAt int64  `json:"totp_updated_at"`
			Status    string `json:"totp_status"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(created.Body.Bytes(), &response))
	assert.Equal(t, int64(1), response.Data.Version)
	assert.Positive(t, response.Data.UpdatedAt)
	assert.Equal(t, "saved", response.Data.Status)
	original := response.Data
	for _, secret := range []string{"", "JBSWY3DPEHPK3PXP", "JBSWY3DPEHPK3PXQ"} {
		updated := gptAccountRequest(t, identity, common.RoleRootUser, original.ID, http.MethodPut, fmt.Sprintf(`{"name":"Primary","email":"owner@example.com","totp_secret":%q}`, secret), UpdateGPTAccount)
		require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
		require.NoError(t, common.Unmarshal(updated.Body.Bytes(), &response))
		if secret == "JBSWY3DPEHPK3PXQ" {
			assert.Equal(t, int64(2), response.Data.Version)
		} else {
			assert.Equal(t, original, response.Data, "empty or unchanged seeds preserve metadata")
		}
		assert.NotContains(t, updated.Body.String(), "JBSWY3DPEHPK3P")
	}
	var account model.GPTAccount
	require.NoError(t, model.DB.First(&account, original.ID).Error)
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "private-password", secrets.Password)
	assert.Equal(t, "JBSWY3DPEHPK3PXQ", secrets.TOTPSecret)
}

func TestGPTAccountTOTPProofPolicy(t *testing.T) {
	identity := setupGPTAccountTest(t)
	for _, scope := range []string{"gpt_account.totp.update", "gpt_account.totp.read"} {
		binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: scope, Context: []byte(`{"account_id":1}`)})
		require.NoError(t, err)
		assert.Equal(t, scope, binding.Scope)
		requirements, err := service.GetVerificationRequirements(identity, scope)
		require.NoError(t, err)
		require.Len(t, requirements.Methods, 1)
		assert.Equal(t, service.VerificationMethodPassword, requirements.Methods[0].Method)
		for _, context := range []string{`{}`, `{"account_id":0}`, `{"account_id":1,"totp_secret":"private"}`} {
			_, err = service.BindVerificationOperation(service.VerificationOperation{Scope: scope, Context: []byte(context)})
			assert.ErrorIs(t, err, service.ErrVerificationContextInvalid)
		}
	}
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: identity.UserID, Secret: "private", IsEnabled: true}).Error)
	for _, scope := range []string{"gpt_account.totp.update", "gpt_account.totp.read"} {
		requirements, err := service.GetVerificationRequirements(identity, scope)
		require.NoError(t, err)
		require.Len(t, requirements.Methods, 1)
		assert.Equal(t, service.VerificationMethodTwoFA, requirements.Methods[0].Method, "password must not bypass an enrolled factor")
	}
}

func TestGPTAccountTOTPRequiresAdminSessionAndOwnership(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", Email: "owner@example.com", SecretID: "session-account", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
	require.NoError(t, model.DB.Create(&account).Error)
	for _, endpoint := range []struct {
		name, body string
		handler    gin.HandlerFunc
	}{
		{"save", `{"totp_secret":"JBSWY3DPEHPK3PXQ","confirmed_bound":true}`, UpdateGPTAccountTOTP},
		{"reveal", `{}`, RevealGPTAccountTOTP},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				role   int
				change func(*gin.Context)
				want   int
			}{
				{"ordinary user", common.RoleCommonUser, nil, http.StatusForbidden},
				{"missing session", common.RoleRootUser, func(c *gin.Context) { c.Set("session_id", "") }, http.StatusForbidden},
				{"missing session version", common.RoleRootUser, func(c *gin.Context) { c.Set("session_version", int64(0)) }, http.StatusForbidden},
				{"wrong account", common.RoleRootUser, func(c *gin.Context) { c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(account.ID + 1)}} }, http.StatusNotFound},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response := gptAccountRequest(t, identity, tc.role, account.ID, http.MethodPost, endpoint.body, func(c *gin.Context) {
						if tc.change != nil {
							tc.change(c)
						}
						endpoint.handler(c)
					})
					assert.Equal(t, tc.want, response.Code)
					assert.NotContains(t, response.Body.String(), "JBSWY3DPEHPK3P")
				})
			}
			require.NoError(t, model.DB.Model(&account).Update("owner_id", identity.UserID+1).Error)
			other := gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodPost, endpoint.body, endpoint.handler)
			assert.Equal(t, http.StatusNotFound, other.Code)
			require.NoError(t, model.DB.Model(&account).Update("owner_id", identity.UserID).Error)
			require.NoError(t, authz.SetUserPermissions(identity.UserID, authz.PermissionsMap{"gpt_account": {"manage": false}}))
			denied := gptAccountRequest(t, identity, common.RoleAdminUser, account.ID, http.MethodPost, endpoint.body, endpoint.handler)
			assert.Equal(t, http.StatusForbidden, denied.Code)
			require.NoError(t, authz.ClearUserPermissions(identity.UserID))
			for _, role := range []int{common.RoleAdminUser, common.RoleRootUser} {
				response := gptAccountRequest(t, identity, role, account.ID, http.MethodPost, endpoint.body, endpoint.handler)
				require.Equal(t, http.StatusOK, response.Code)
			}
		})
	}
}

func TestGPTAccountTOTPConfirmedSaveAndReveal(t *testing.T) {
	identity := setupGPTAccountTest(t)
	channel := model.Channel{Type: 57, Key: `{"access_token":"private-access","refresh_token":"private-refresh","account_id":"account"}`, Name: "Preserved", Status: common.ChannelStatusEnabled, Models: "gpt-5", Group: "default"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", Email: "owner@example.com", SecretID: "saved-account", Generation: 1, Group: "default", Models: "gpt-5", ChannelID: &channel.Id, ChannelKeyHash: model.GPTChannelKeyHash(channel.Key), AccountID: "account", OAuthSubject: "subject", AutoRelogin: true, Status: "ready"}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
	require.NoError(t, model.DB.Create(&account).Error)
	legacy := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
	assert.Contains(t, legacy.Body.String(), `"totp_status":"saved"`)
	job, err := service.AcquireGPTAccountJob(account.ID, false)
	require.NoError(t, err)
	before := time.Now().Unix()
	saved := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{"totp_secret":"jbsw y3dp ehpk 3pxq","confirmed_bound":true}`, UpdateGPTAccountTOTP)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	assert.Contains(t, saved.Body.String(), `"totp_status":"operator_confirmed"`)
	assert.NotContains(t, saved.Body.String(), "JBSWY3DPEHPK3P")
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, int64(1), account.TOTPVersion)
	assert.GreaterOrEqual(t, account.TOTPUpdatedAt, before)
	assert.Equal(t, job.Generation+1, account.Generation)
	assert.Empty(t, account.LeaseID)
	assert.Zero(t, account.LeaseUntil)
	assert.Equal(t, "account", account.AccountID)
	assert.Equal(t, "subject", account.OAuthSubject)
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "private-password", secrets.Password)
	assert.Equal(t, "JBSWY3DPEHPK3PXQ", secrets.TOTPSecret)
	assert.NotContains(t, account.SecretsCiphertext, secrets.TOTPSecret)
	assert.ErrorIs(t, model.StoreGPTAccountCredential(job, `{"access_token":"stale"}`, "account"), model.ErrGPTAccountStale)
	service.ReleaseGPTAccountJob(job, "invalid_credentials")
	var storedChannel model.Channel
	require.NoError(t, model.DB.First(&storedChannel, channel.Id).Error)
	assert.Equal(t, channel.Key, storedChannel.Key)
	assert.Equal(t, channel.Status, storedChannel.Status)
	for _, seed := range []string{"", "JBSWY3DPEHPK3PXQ"} {
		updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, fmt.Sprintf(`{"name":"Managed","email":"owner@example.com","totp_secret":%q}`, seed), UpdateGPTAccount)
		require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
		assert.Contains(t, updated.Body.String(), `"totp_status":"operator_confirmed"`)
		assert.Contains(t, updated.Body.String(), `"totp_version":1`)
	}
	revealed := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{}`, RevealGPTAccountTOTP)
	require.Equal(t, http.StatusOK, revealed.Code, revealed.Body.String())
	assert.Contains(t, revealed.Header().Get("Cache-Control"), "no-store")
	var response struct {
		Data struct {
			Secret    string `json:"totp_secret"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(revealed.Body.Bytes(), &response))
	assert.Equal(t, "JBSWY3DPEHPK3PXQ", response.Data.Secret)
	assert.InDelta(t, time.Now().Unix()+30, response.Data.ExpiresAt, 1)
	listed := gptAccountRequest(t, identity, common.RoleRootUser, 0, http.MethodGet, "", ListGPTAccounts)
	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	for _, secret := range []string{"JBSWY3DPEHPK3PXP", "JBSWY3DPEHPK3PXQ", "private-password", "private-access", "private-refresh"} {
		assert.NotContains(t, listed.Body.String(), secret)
		assert.NotContains(t, string(encoded), secret)
	}
	assert.Contains(t, string(encoded), "gpt_account.totp.update")
	assert.Contains(t, string(encoded), "gpt_account.totp.read")
}

func TestGPTAccountTOTPInvalidSaveAndEncryptionFailurePreserveState(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", Email: "owner@example.com", SecretID: "invalid-account", Generation: 7}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "private-password"}))
	require.NoError(t, model.DB.Create(&account).Error)
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	original := account
	for _, body := range []string{`{}`, `{"totp_secret":"JBSWY3DPEHPK3PXQ"}`, `{"totp_secret":"JBSWY3DPEHPK3PXQ","confirmed_bound":false}`, `{"totp_secret":"","confirmed_bound":true}`, `{"totp_secret":"AAAAAAAAAAAAAAAAAB","confirmed_bound":true}`, `{"totp_secret":"JBSWY3DPEHPK3PXQ=","confirmed_bound":true}`} {
		response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, body, UpdateGPTAccountTOTP)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	empty := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{}`, RevealGPTAccountTOTP)
	assert.Equal(t, http.StatusNotFound, empty.Code)
	t.Setenv("GPT_ACCOUNT_ENCRYPTION_KEY_FILE", "")
	for _, handler := range []struct {
		fn   gin.HandlerFunc
		body string
	}{
		{UpdateGPTAccountTOTP, `{"totp_secret":"JBSWY3DPEHPK3PXQ","confirmed_bound":true}`},
		{RevealGPTAccountTOTP, `{}`},
	} {
		response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, handler.body, handler.fn)
		assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		assert.NotContains(t, response.Body.String(), "JBSWY3DPEHPK3P")
	}
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Equal(t, original, account)
}

func TestGPTAccountTOTPConcurrentCredentialChange(t *testing.T) {
	for _, reveal := range []bool{false, true} {
		t.Run(fmt.Sprintf("reveal_%v", reveal), func(t *testing.T) {
			identity := setupGPTAccountTest(t)
			account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", Email: "owner@example.com", SecretID: "concurrent-account", Generation: 1}
			require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{Password: "old-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}))
			require.NoError(t, model.DB.Create(&account).Error)
			// A concurrent general edit commits after the first account read, before
			// the dedicated operation can write or reveal.
			raceRan := false
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register("test:concurrent-gpt-secret", func(tx *gorm.DB) {
				if snapshot, ok := tx.Statement.Dest.(*model.GPTAccount); !ok || snapshot.ID != account.ID || raceRan {
					return
				}
				raceRan = true
				updated := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPut, `{"name":"Managed","email":"owner@example.com","password":"concurrent-password","totp_secret":"JBSWY3DPEHPK3PXQ"}`, UpdateGPTAccount)
				require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
			}))
			t.Cleanup(func() { _ = model.DB.Callback().Query().Remove("test:concurrent-gpt-secret") })
			handler, body := UpdateGPTAccountTOTP, `{"totp_secret":"AAAAAAAAAAAAAAAA","confirmed_bound":true}`
			if reveal {
				handler, body = RevealGPTAccountTOTP, `{}`
			}
			response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, body, handler)
			require.True(t, raceRan)
			if reveal {
				assert.Equal(t, http.StatusOK, response.Code)
				assert.Contains(t, response.Body.String(), "JBSWY3DPEHPK3PXQ")
				assert.NotContains(t, response.Body.String(), "JBSWY3DPEHPK3PXP")
			} else {
				assert.Equal(t, http.StatusConflict, response.Code)
			}
			require.NoError(t, model.DB.First(&account, account.ID).Error)
			secrets, err := service.DecryptGPTAccountSecrets(&account)
			require.NoError(t, err)
			assert.Equal(t, "concurrent-password", secrets.Password)
			assert.Equal(t, "JBSWY3DPEHPK3PXQ", secrets.TOTPSecret)
			assert.Equal(t, int64(1), account.TOTPVersion)
			assert.Equal(t, "saved", account.TOTPStatus)
		})
	}
}

func TestGPTAccountSessionActionsReachServiceWithoutStepUp(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: "direct-actions", Generation: 1, Status: "pending"}
	require.NoError(t, model.DB.Create(&account).Error)
	for _, tc := range []struct {
		name, code string
		handler    gin.HandlerFunc
	}{
		{"login", "automation_unavailable", LoginGPTAccount},
		{"refresh", "channel_required", RefreshGPTAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := gptAccountRequest(t, identity, common.RoleRootUser, account.ID, http.MethodPost, `{}`, tc.handler)
			require.Equal(t, http.StatusConflict, response.Code)
			var result struct {
				Code string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, tc.code, result.Code)
		})
	}
}

func TestGPTAccountMutationsRejectInvalidDashboardSessions(t *testing.T) {
	identity := setupGPTAccountTest(t)
	account := model.GPTAccount{OwnerID: identity.UserID, Name: "Managed", Email: "owner@example.com", SecretID: "session-guard", Generation: 1}
	require.NoError(t, service.EncryptGPTAccountSecrets(&account, service.GPTAccountSecrets{TOTPSecret: "JBSWY3DPEHPK3PXP"}))
	require.NoError(t, model.DB.Create(&account).Error)
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	suffix, err := common.GenerateRandomCharsKey(43)
	require.NoError(t, err)
	pat := model.AccessTokenPrefix + suffix
	patRecord := &model.UserAccessToken{Name: "GPT test", TokenHash: model.AccessTokenFingerprint(pat), TokenHint: model.AccessTokenHint(pat), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, patRecord.SetScopes([]string{"gpt_account:manage"}))
	require.NoError(t, model.CreateUserAccessToken(identity.UserID, patRecord, service.AccessTokenMaxPerUser))
	endpoints := []struct {
		method, path string
		handler      gin.HandlerFunc
	}{
		{http.MethodPost, "/api/gpt-accounts", CreateGPTAccount},
		{http.MethodPut, "/api/gpt-accounts/config", UpdateGPTAccountsConfig},
		{http.MethodPost, "/api/gpt-accounts/import-oauth", ImportGPTAccountOAuth},
		{http.MethodPut, "/api/gpt-accounts/:id", UpdateGPTAccount},
		{http.MethodDelete, "/api/gpt-accounts/:id", DeleteGPTAccount},
		{http.MethodPost, "/api/gpt-accounts/:id/totp", UpdateGPTAccountTOTP},
		{http.MethodPost, "/api/gpt-accounts/:id/totp/reveal", RevealGPTAccountTOTP},
		{http.MethodPost, "/api/gpt-accounts/:id/login", LoginGPTAccount},
		{http.MethodGet, "/api/gpt-accounts/:id/oauth", StartGPTAccountOAuth},
		{http.MethodPost, "/api/gpt-accounts/:id/callback", CompleteGPTAccountOAuth},
		{http.MethodPost, "/api/gpt-accounts/:id/import-oauth", ImportGPTAccountOAuthForAccount},
		{http.MethodPost, "/api/gpt-accounts/:id/refresh", RefreshGPTAccount},
		{http.MethodPost, "/api/gpt-accounts/:id/sync", SyncGPTAccountGateway},
		{http.MethodPost, "/api/gpt-account-gateways", CreateGPTAccountGateway},
		{http.MethodPut, "/api/gpt-account-gateways/:id", UpdateGPTAccountGateway},
		{http.MethodPost, "/api/gpt-account-gateways/import-channel-json", ImportGPTGatewayChannelJSON},
	}
	engine := gin.New()
	engine.Use(middleware.AdminAuth())
	for _, endpoint := range endpoints {
		middleware.DeclareAccessTokenPermissionRoute(endpoint.method, endpoint.path, authz.GPTAccountManage)
		engine.Handle(endpoint.method, endpoint.path, middleware.RequirePermission(authz.GPTAccountManage), endpoint.handler)
	}
	permitted := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/gpt-accounts/%d/totp/reveal", account.ID), strings.NewReader(`{}`))
	permitted.Header.Set("Authorization", "Bearer "+token)
	permittedResponse := httptest.NewRecorder()
	engine.ServeHTTP(permittedResponse, permitted)
	require.Equal(t, http.StatusOK, permittedResponse.Code)
	assert.Contains(t, permittedResponse.Header().Get("Cache-Control"), "no-store")
	for _, tc := range []struct {
		name, credential string
		status           int
		changes          map[string]any
	}{
		{"anonymous", "", http.StatusUnauthorized, nil},
		{"personal access token", pat, http.StatusForbidden, nil},
		{"expired session", token, http.StatusUnauthorized, map[string]any{"expires_at": time.Now().Add(-time.Minute).Unix()}},
		{"revoked session", token, http.StatusUnauthorized, map[string]any{"expires_at": time.Now().Add(time.Hour).Unix(), "status": model.UserSessionStatusRevoked}},
		{"changed session version", token, http.StatusUnauthorized, map[string]any{"status": model.UserSessionStatusActive, "version": identity.SessionVersion + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.changes != nil {
				require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", identity.SessionID).Updates(tc.changes).Error)
			}
			for _, endpoint := range endpoints {
				req := httptest.NewRequest(endpoint.method, strings.ReplaceAll(endpoint.path, ":id", strconv.Itoa(account.ID)), strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				if tc.credential != "" {
					req.Header.Set("Authorization", "Bearer "+tc.credential)
				}
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, req)
				assert.Equal(t, tc.status, response.Code, endpoint.path)
				if tc.name == "personal access token" {
					assert.Contains(t, response.Body.String(), "authorization_forbidden", endpoint.path)
				}
				assert.NotContains(t, response.Body.String(), "JBSWY3DPEHPK3P")
			}
		})
	}
	require.NoError(t, model.DB.First(&account, account.ID).Error)
	assert.Zero(t, account.TOTPVersion)
	secrets, err := service.DecryptGPTAccountSecrets(&account)
	require.NoError(t, err)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", secrets.TOTPSecret)
}
