package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestModelPricingRoutesRequireRootSession(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
	databasePath := filepath.Join(t.TempDir(), "pricing-routes.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Option{}, &model.AuditLog{}))
	require.NoError(t, sqlDB.Close())
	t.Setenv("SQL_DSN", "local")
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.SQLitePath = false, databasePath
	require.NoError(t, model.InitDB())
	db, model.LOG_DB = model.DB, model.DB
	sqlDB, err = db.DB()
	require.NoError(t, err)
	common.RedisEnabled, common.SessionSecret = false, "model-pricing-route-test-secret"
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite
		require.NoError(t, sqlDB.Close())
	})
	engine := newAccessTokenScopeTestEngine(t)
	for _, actor := range []struct {
		name    string
		role    int
		expired bool
		status  int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "admin", role: common.RoleAdminUser, status: http.StatusForbidden},
		{name: "expired-root", role: common.RoleRootUser, expired: true, status: http.StatusUnauthorized},
		{name: "root", role: common.RoleRootUser},
	} {
		t.Run(actor.name, func(t *testing.T) {
			var token string
			if actor.role != 0 {
				user := &model.User{Username: "pricing-" + actor.name, AffCode: actor.name, Role: actor.role, Status: common.UserStatusEnabled, AuthVersion: 1}
				require.NoError(t, db.Create(user).Error)
				expiresAt := time.Now().Add(time.Hour).Unix()
				if actor.expired {
					expiresAt = time.Now().Add(-time.Minute).Unix()
				}
				session := &model.UserSession{SID: "pricing-" + actor.name, UserID: user.Id, Version: 1, UserAuthVersion: 1,
					Status: model.UserSessionStatusActive, RefreshHash: "unused-test-hash", LoginMethod: "password", ExpiresAt: expiresAt}
				require.NoError(t, db.Create(session).Error)
				token, _, err = service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1})
				require.NoError(t, err)
			}
			for _, endpoint := range []struct {
				method, path string
				rootStatus   int
			}{
				{http.MethodGet, "/api/option/model_pricing?model=route-test-model", http.StatusOK},
				{http.MethodPatch, "/api/option/model_pricing", http.StatusBadRequest},
				{http.MethodPost, "/api/option/model_pricing/convert", http.StatusBadRequest},
				{http.MethodPost, "/api/option/model_pricing/preview", http.StatusBadRequest},
			} {
				t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
					// Invalid JSON exercises write-handler validation without changing prices.
					request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("{"))
					if token != "" {
						request.Header.Set("Authorization", "Bearer "+token)
					}
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, request)
					wantStatus := actor.status
					if wantStatus == 0 {
						wantStatus = endpoint.rootStatus
					}
					require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
					if wantStatus == http.StatusOK {
						var response struct {
							Success bool                       `json:"success"`
							Data    model.ModelPricingSnapshot `json:"data"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
						assert.True(t, response.Success)
						require.Len(t, response.Data.Entries, 1)
						assert.Equal(t, "route-test-model", response.Data.Entries[0].ModelName)
					}
				})
			}
		})
	}
}

func TestChannelMonitorRoutesRequireAdministratorSession(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousRateLimit := common.GlobalApiRateLimitEnable
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.UserAccessToken{}, &model.Option{}, &model.AuditLog{}, &model.Channel{}, &model.ChannelMonitorArtifact{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled, common.SessionSecret = false, "channel-monitor-route-test-secret"
	common.GlobalApiRateLimitEnable = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.GlobalApiRateLimitEnable = previousRateLimit
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
		require.NoError(t, sqlDB.Close())
	})
	engine := newAccessTokenScopeTestEngine(t)
	for _, actor := range []struct {
		name         string
		role         int
		deniedStatus int
	}{
		{name: "anonymous", deniedStatus: http.StatusUnauthorized},
		{name: "user", role: common.RoleCommonUser, deniedStatus: http.StatusForbidden},
		{name: "admin", role: common.RoleAdminUser},
		{name: "root", role: common.RoleRootUser},
	} {
		t.Run(actor.name, func(t *testing.T) {
			var token string
			if actor.role != 0 {
				user := &model.User{Username: "monitor-" + actor.name, AffCode: actor.name, Role: actor.role, Status: common.UserStatusEnabled, AuthVersion: 1}
				require.NoError(t, db.Create(user).Error)
				session := &model.UserSession{SID: "monitor-" + actor.name, UserID: user.Id, Version: 1, UserAuthVersion: 1,
					Status: model.UserSessionStatusActive, RefreshHash: "unused-test-hash", LoginMethod: "password", ExpiresAt: time.Now().Add(time.Hour).Unix()}
				require.NoError(t, db.Create(session).Error)
				token, _, err = service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1})
				require.NoError(t, err)
			}
			endpoints := []struct {
				method, path  string
				allowedStatus int
			}{
				{http.MethodGet, "/api/channel-monitor", http.StatusOK},
				{http.MethodGet, "/api/channel-monitor/config", http.StatusOK},
				{http.MethodPut, "/api/channel-monitor/config", http.StatusBadRequest},
			}
			if actor.deniedStatus != 0 {
				endpoints = append(endpoints, struct {
					method, path  string
					allowedStatus int
				}{http.MethodPost, "/api/channel-monitor/run", 0})
			}
			for _, endpoint := range endpoints {
				t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
					// Invalid JSON reaches authorized configuration validation without changing settings.
					request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("{"))
					if token != "" {
						request.Header.Set("Authorization", "Bearer "+token)
					}
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, request)
					wantStatus := actor.deniedStatus
					if wantStatus == 0 {
						wantStatus = endpoint.allowedStatus
					}
					require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
					var response struct {
						Success bool           `json:"success"`
						Data    map[string]any `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					if wantStatus == http.StatusOK {
						assert.True(t, response.Success)
						assert.NotNil(t, response.Data)
						if endpoint.path == "/api/channel-monitor/config" {
							assert.Contains(t, response.Data, "config")
						}
					} else {
						assert.False(t, response.Success)
						assert.Nil(t, response.Data)
					}
				})
			}
		})
	}

	t.Run("scoped access tokens", func(t *testing.T) {
		user := &model.User{Username: "monitor-token-root", AffCode: "monitor-token", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
		require.NoError(t, db.Create(user).Error)
		for _, scope := range []string{"ops:read", "ops:write"} {
			suffix, err := common.GenerateRandomCharsKey(43)
			require.NoError(t, err)
			raw := model.AccessTokenPrefix + suffix
			token := &model.UserAccessToken{Name: scope, TokenHash: model.AccessTokenFingerprint(raw), TokenHint: model.AccessTokenHint(raw)}
			require.NoError(t, token.SetScopes([]string{scope}))
			require.NoError(t, model.CreateUserAccessToken(user.Id, token, service.AccessTokenMaxPerUser))
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				t.Run(scope+" "+method, func(t *testing.T) {
					request := httptest.NewRequest(method, "/api/channel-monitor/config", strings.NewReader("{"))
					request.Header.Set("Authorization", "Bearer "+raw)
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, request)
					status := http.StatusForbidden
					if method == http.MethodGet && scope == "ops:read" {
						status = http.StatusOK
					} else if method == http.MethodPut && scope == "ops:write" {
						status = http.StatusBadRequest
					}
					require.Equal(t, status, recorder.Code, recorder.Body.String())
					if status == http.StatusForbidden {
						assert.Contains(t, recorder.Body.String(), "ACCESS_TOKEN_SCOPE_DENIED")
					}
				})
			}
		}
	})
}

func TestGPTAccountPollingPreservesSensitiveOperationRateLimit(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSecret := common.RedisEnabled, common.SessionSecret
	previousRedisClient := common.RDB
	previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
	previousCritical, previousLimit, previousDuration := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	databasePath := filepath.Join(t.TempDir(), "gpt-polling-routes.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Option{}, &model.AuditLog{}, &model.GPTAccount{}, &model.GPTAccountGateway{}, &model.Channel{}))
	require.NoError(t, sqlDB.Close())
	t.Setenv("SQL_DSN", "local")
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.SQLitePath = false, databasePath
	require.NoError(t, model.InitDB())
	db, model.LOG_DB = model.DB, model.DB
	sqlDB, err = db.DB()
	require.NoError(t, err)
	rateLimitServer := miniredis.RunT(t)
	rateLimitClient := redis.NewClient(&redis.Options{Addr: rateLimitServer.Addr()})
	common.RDB = rateLimitClient
	common.RedisEnabled, common.SessionSecret = true, "gpt-polling-route-test-secret"
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = true, 2, 60
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.SessionSecret = previousRedis, previousSecret
		common.RDB = previousRedisClient
		common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = previousCritical, previousLimit, previousDuration
		require.NoError(t, sqlDB.Close())
		require.NoError(t, rateLimitClient.Close())
	})
	user := model.User{Username: "gpt-route-root", AffCode: "gpt-route", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	session := model.UserSession{SID: "gpt-route-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "unused-test-hash", LoginMethod: "password", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, db.Create(&session).Error)
	token, _, err := service.IssueAccessToken(service.AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	api := engine.Group("/api")
	registerGPTAccountRoutes(api)
	api.POST("/test-sensitive", middleware.CriticalRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := func(method, path, credential, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.RemoteAddr = ip + ":12345"
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, r)
		return response
	}
	const pollIP = "192.0.2.251"
	reads := []string{"/api/gpt-accounts", "/api/gpt-accounts/config", "/api/gpt-account-gateways"}
	for range 3 {
		for _, path := range reads {
			response := request(http.MethodGet, path, token, pollIP)
			require.Equal(t, http.StatusOK, response.Code, "%s: %s", path, response.Body.String())
		}
	}
	assert.Equal(t, http.StatusNoContent, request(http.MethodPost, "/api/test-sensitive", "", pollIP).Code)
	assert.Equal(t, http.StatusNoContent, request(http.MethodPost, "/api/test-sensitive", "", pollIP).Code)
	limited := request(http.MethodPost, "/api/test-sensitive", "", pollIP)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.NotEmpty(t, limited.Header().Get("Retry-After"))
	for _, path := range reads {
		assert.Equal(t, http.StatusOK, request(http.MethodGet, path, token, pollIP).Code, path)
		assert.Equal(t, http.StatusUnauthorized, request(http.MethodGet, path, "", pollIP).Code, path)
	}
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/gpt-accounts/1/oauth"},
		{http.MethodPost, "/api/gpt-accounts/1/login"},
		{http.MethodPost, "/api/gpt-accounts/1/totp/reveal"},
		{http.MethodPost, "/api/gpt-account-gateways"},
		{http.MethodPut, "/api/gpt-accounts/config"},
	} {
		assert.Equal(t, http.StatusTooManyRequests, request(endpoint.method, endpoint.path, token, pollIP).Code, endpoint.path)
	}
	assert.Equal(t, http.StatusNotFound, request(http.MethodGet, "/api/gpt-accounts/1/oauth", token, "192.0.2.252").Code)
	assert.Equal(t, http.StatusNotFound, request(http.MethodPost, "/api/gpt-accounts/1/login", token, "192.0.2.253").Code)
	assert.Equal(t, http.StatusTooManyRequests, request(http.MethodPost, "/api/gpt-accounts/1/totp/reveal", token, "192.0.2.254").Code,
		"changing IP must not bypass the authenticated user's sensitive-operation limit")
}

// accessTokenExemptRoutes are /api routes that never reach dashboard
// authentication, so a personal access token has no rule to satisfy there.
var accessTokenExemptRoutes = []string{
	// Partner endpoints validate their own signed partner credentials.
	"POST /api/partner/v1/redemption-codes",
	"POST /api/partner/v1/redemption-orders/query",
	"POST /api/partner/v1/redemption-codes/revoke",
	// Anonymous pages and setup.
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/notice",
	"GET /api/user-agreement",
	"GET /api/privacy-policy",
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/ratio_config",
	"GET /api/user/groups",

	// Anonymous account entry points.
	"GET /api/verification",
	"GET /api/reset_password",
	"POST /api/user/reset",
	"POST /api/user/register",
	"GET /api/user/login/encryption-key",
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/login/verify",
	"POST /api/user/login/passkey/begin",
	"POST /api/user/login/passkey/finish",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	"GET /api/oauth/telegram/bind/:flow_token",

	// Browser session cookie endpoints.
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",

	// Payment callbacks.
	"POST /api/stripe/webhook",
	"POST /api/creem/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",

	// API key authentication (TokenAuthReadOnly).
	"GET /api/usage/token/",
	"GET /api/log/token",
}

// accessTokenHelperRoutes are declared through handlePermissionRoute outside
// channelPermissionRoutes.
var accessTokenHelperRoutes = map[string]authz.Permission{
	"GET /api/task_plugin_options":                       authz.TaskPluginBind,
	"GET /api/audit":                                     authz.AuditRead,
	"POST /api/gpt-account-gateways/import-channel-json": authz.GPTAccountManage,
	"PUT /api/gpt-accounts/config":                       authz.GPTAccountManage,
}

func newAccessTokenScopeTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// A permission route that is also in the static table panics here.
	require.NotPanics(t, func() {
		SetApiRouter(engine)
		SetDashboardRouter(engine)
		SetRelayRouter(engine)
		SetTaskPluginProtocolRouter(engine)
		SetVideoRouter(engine)
		SetTaskRouter(engine)
	})
	return engine
}

func TestAccessTokenRouteRulesCoverEveryDashboardRoute(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	exempt := map[string]bool{}
	for _, key := range accessTokenExemptRoutes {
		exempt[key] = true
	}
	registered := map[string]bool{}
	panel := map[string]bool{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") && !strings.HasPrefix(route.Path, "/pg/") {
			continue
		}
		key := route.Method + " " + route.Path
		registered[key] = true
		if !exempt[key] {
			panel[key] = true
		}
	}
	for _, key := range accessTokenExemptRoutes {
		assert.True(t, registered[key], "exempt route %s is no longer registered", key)
	}

	declared := map[string]int{}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		declared[key]++
	}
	for key := range panel {
		assert.Equal(t, 1, declared[key], "panel route %s must be declared exactly once", key)
	}
	for key := range declared {
		assert.True(t, panel[key], "declared route %s is not a registered panel route", key)
	}

	permissionRoutes := map[string]authz.Permission{}
	for _, route := range channelPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/channel", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		permissionRoutes[key] = permission
	}
	for key, permission := range permissionRoutes {
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, "scope", rule.Kind(), key)
		assert.Equal(t, service.AccessTokenScopeOf(permission), rule.Scope(), key)
	}
}

func TestAccessTokenCatalogCoversEveryPermission(t *testing.T) {
	catalog := service.AccessTokenCatalog(1, common.RoleRootUser)
	scopes := map[string]bool{}
	resources := map[string]int{}
	for _, group := range catalog {
		for _, resource := range group.Resources {
			resources[resource.Resource]++
			for _, action := range resource.Actions {
				scopes[resource.Resource+":"+action.Action] = true
			}
		}
	}
	for _, permission := range authz.AllPermissions() {
		assert.True(t, scopes[service.AccessTokenScopeOf(permission)], "root catalog lacks %s", service.AccessTokenScopeOf(permission))
	}
	for resource, count := range resources {
		assert.Equal(t, 1, count, "resource %s appears in more than one catalog entry", resource)
	}
}

func TestAccessTokenVerificationScopesAreAllListed(t *testing.T) {
	files, err := filepath.Glob("../service/*.go")
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	var scopes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "VerificationScope") || i >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, name.Name)
					scope, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					scopes = append(scopes, scope)
				}
			}
		}
	}
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		_, listed := service.AccessTokenVerificationScope(scope)
		assert.True(t, listed, "verification scope %s has no access token rule", scope)
	}
	// Channel JSON import requires a root browser session, even if a PAT has
	// the route's gpt_account:manage permission.
	required, listed := service.AccessTokenVerificationScope(service.VerificationScopeGPTGatewayChannelImport)
	require.True(t, listed)
	assert.Empty(t, required)
}
