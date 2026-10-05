package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManageUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled, previousSecret := common.RedisEnabled, common.SessionSecret
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	databaseTypes := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	require.Contains(t, databaseTypes, dialect)
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	logDB := db
	if os.Getenv("TEST_MANAGE_USER_SEPARATE_LOG_DB") == "1" {
		logDB, _ = newAuditTestDatabase(t, dialect, dsn)
	}
	model.DB, model.LOG_DB = db, logDB
	common.RedisEnabled = false
	common.SessionSecret = "manage-user-test-secret"
	common.SetDatabaseTypes(databaseTypes[dialect], databaseTypes[dialect])

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.SessionSecret = previousRedisEnabled, previousSecret
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if logDB != db {
			sqlLogDB, err := logDB.DB()
			if err == nil {
				_ = sqlLogDB.Close()
			}
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.UserAccessToken{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s, separate log database: %v", dialect, version, logDB != db)
	return db
}

func performManageUserRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performVerifiedManageUserRequest(t, body, service.AuthIdentity{UserID: 9999}, "")
}

// performVerifiedManageUserRequest drives ManageUser as the root operator from
// a dashboard login session, carrying the step-up proof status or role changes need.
func performVerifiedManageUserRequest(t *testing.T, body string, identity service.AuthIdentity, proof string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		c.Request.Header.Set("X-Security-Proof", proof)
	}
	c.Set("id", identity.UserID)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "root-operator")
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
	c.Set(common.RequestIdKey, "quota-test-request")
	ManageUser(c)
	return recorder
}

// manageUserProof signs the root operator in and issues a password-method proof
// bound to one ManageUser operation.
func manageUserProof(t *testing.T, db *gorm.DB, operation service.VerificationOperation) (service.AuthIdentity, string) {
	t.Helper()
	operator := createQuotaTestOperator(t, db, common.RoleRootUser)
	require.NoError(t, model.PublishUserAuthCache(operator.Id))
	bundle, err := service.CreateLoginSession(operator.Id, "password", "127.0.0.1", "manage-user-test")
	require.NoError(t, err)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	binding, err := service.BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	return identity, proof
}

func TestManageUserDisableAdvancesAuthVersionOnceAndRevokesSession(t *testing.T) {
	db := setupManageUserTestDB(t)
	now := time.Now().Unix()
	user := model.User{
		Username: "managed-disable-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "managed-disable-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"disable"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, updated.Status)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "managed-disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
}

func TestManageUserDemoteAdvancesAuthVersionAndRevokesSessionsOnce(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))

	now := time.Now().Unix()
	user := model.User{
		Username: "managed-demote-user", Password: "password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	for _, sid := range []string{"managed-demote-session-one", "managed-demote-session-two"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password",
			LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}

	sessionUpdateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_demote_session_updates", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionUpdateCount++
		}
	}))

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"demote"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"demote"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("sid asc").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	assert.Equal(t, 1, sessionUpdateCount)
}

func TestManageUserDeleteReturnsImmediatelyAndUnknownActionFails(t *testing.T) {
	db := setupManageUserTestDB(t)
	deleted := model.User{
		Username: "managed-delete-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "delete-aff",
	}
	require.NoError(t, db.Create(&deleted).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserDelete, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, deleted.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, deleted.Id), identity, proof)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var deletedCount int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.Id).Count(&deletedCount).Error)
	assert.EqualValues(t, 1, deletedCount)

	unchanged := model.User{
		Username: "managed-unknown-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "unknown-aff",
	}
	require.NoError(t, db.Create(&unchanged).Error)
	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"unknown"}`, unchanged.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&unchanged, unchanged.Id).Error)
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, unchanged.Status)
}

func createQuotaTestOperator(t *testing.T, db *gorm.DB, role int) model.User {
	t.Helper()
	if role == 0 {
		role = common.RoleRootUser
	}
	operator := model.User{Id: 9999, Username: "root-operator", Password: "root-operator-hash", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "root-operator-aff"}
	require.NoError(t, db.Create(&operator).Error)
	return operator
}

func TestManageUserQuotaRecordsTopupAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name, mode, action, content string
		value, wantQuota            int
	}{
		{"add", "add", "user.quota_add", "Increased user quota by 500", 500, 1500},
		{"subtract", "subtract", "user.quota_subtract", "Decreased user quota by 500", 500, 500},
		{"override_up", "override", "user.quota_override", "Overrode user quota from 1000 to 1500", 1500, 1500},
		{"override_down", "override", "user.quota_override", "Overrode user quota from 1000 to 500", 500, 500},
		{"override_unchanged", "override", "user.quota_override", "Overrode user quota from 1000 to 1000", 1000, 1000},
		{"override_zero", "override", "user.quota_override", "Overrode user quota from 1000 to 0", 0, 0},
		{"override_negative", "override", "user.quota_override", "Overrode user quota from 1000 to -1", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000, AffCode: "quota-owner-aff"}
			require.NoError(t, db.Create(&user).Error)
			createQuotaTestOperator(t, db, common.RoleRootUser)

			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.wantQuota, user.Quota)

			logs, total, err := model.GetAllLogs(model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			assert.Equal(t, user.Id, logs[0].UserId)
			assert.Equal(t, user.Username, logs[0].Username)
			assert.Equal(t, tc.content, logs[0].Content)
			model.FormatAdminLogs(logs)
			var other model.AuditOther
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Equal(t, &model.AuditAdminInfo{AdminID: 9999, AdminUsername: "root-operator", AdminRole: common.RoleRootUser, AuthMethod: "session"}, other.AdminInfo)

			logs, total, err = model.GetUserLogs(user.Id, model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, logs, 1)
			other = model.AuditOther{}
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
			assert.Nil(t, other.AdminInfo)
			require.NotNil(t, other.Op)
			assert.Equal(t, tc.action, other.Op.Action)
			params, err := common.Marshal(other.Op.Params)
			require.NoError(t, err)
			expectedParams := model.AuditFields{"target_user_id": user.Id, "target_username": user.Username, "mode": tc.mode, "requested_quota": tc.value, "from": 1000, "to": tc.wantQuota}
			if tc.mode != "override" {
				expectedParams["quota"] = tc.value
			}
			expected, err := common.Marshal(expectedParams)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", logs[0].RequestId)
			assert.Empty(t, logs[0].Ip, "recipient logs must not disclose the administrator IP")

			logs, total, err = model.GetUserLogs(9999, model.LogFilter{Type: model.LogTypeTopup}, 0, 20)
			require.NoError(t, err)
			assert.Zero(t, total)
			assert.Empty(t, logs)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.Equal(t, 9999, audits[0].UserId)
			assert.Equal(t, "root-operator", audits[0].Username)
			assert.Equal(t, tc.action, audits[0].Action)
			assert.True(t, audits[0].Success)
			params, err = common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.Equal(t, "quota-test-request", audits[0].RequestId)
		})
	}
}

func TestManageUserQuotaFailuresDoNotRecordTopup(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		value      int
		failUpdate bool
	}{
		{"zero_add", "add", 0, false},
		{"negative_subtract", "subtract", -1, false},
		{"invalid_mode", "invalid", 500, false},
		{"add_update_error", "add", 500, true},
		{"subtract_update_error", "subtract", 500, true},
		{"override_update_error", "override", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:fail_quota_update", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota update unavailable"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1000, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			assert.Zero(t, logCount)
			assert.EqualValues(t, 1, auditCount)
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.First(&audit).Error)
			assert.False(t, audit.Success)
			params, err := common.Marshal(audit.Other.Op.Params)
			require.NoError(t, err)
			assert.NotContains(t, string(params), `"from"`)
			assert.NotContains(t, string(params), `"to"`)
			assert.NotContains(t, string(params), `"target_username"`)
			assert.NotContains(t, string(params), "quota update unavailable")
			reason := "invalid_parameters"
			if tc.failUpdate {
				reason = "database_error"
			}
			assert.Contains(t, string(params), `"failure_reason":"`+reason+`"`)
			assert.Contains(t, string(params), fmt.Sprintf(`"requested_quota":%d`, tc.value))
		})
	}
}

func TestManageUserQuotaLogFailureKeepsSuccessfulAdjustment(t *testing.T) {
	for _, failedTable := range []string{"logs", "audit_logs"} {
		t.Run(failedTable, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, common.RoleRootUser)
			user := model.User{Username: "quota-owner", Role: common.RoleCommonUser, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, model.LOG_DB.Callback().Create().Before("gorm:create").Register("test:fail_quota_log", func(tx *gorm.DB) {
				if tx.Statement.Table == failedTable {
					tx.AddError(errors.New("quota log unavailable"))
				}
			}))
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":500}`, user.Id))
			assert.Contains(t, recorder.Body.String(), `"success":true`)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1500, user.Quota)
			var logCount, auditCount int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error)
			require.NoError(t, model.LOG_DB.Model(&model.AuditLog{}).Count(&auditCount).Error)
			if failedTable == "logs" {
				assert.Zero(t, logCount)
				assert.EqualValues(t, 1, auditCount)
			} else {
				assert.EqualValues(t, 1, logCount)
				assert.Zero(t, auditCount)
			}
		})
	}
}

func TestManageUserQuotaTargetsAndWalletBounds(t *testing.T) {
	for _, tc := range []struct {
		name, mode, reason                                string
		before, value, targetID, targetRole, operatorRole int
		deleted, failRead                                 bool
	}{
		{name: "zero_id", mode: "add", value: 1, reason: "invalid_parameters"},
		{name: "negative_id", mode: "subtract", value: 1, targetID: -1, reason: "invalid_parameters"},
		{name: "missing", mode: "override", value: 1, targetID: 12345, reason: "target_not_found"},
		{name: "deleted", mode: "add", value: 1, targetID: 1, deleted: true, reason: "target_not_found"},
		{name: "peer_admin", mode: "add", value: 1, targetID: 1, targetRole: common.RoleAdminUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "higher_role", mode: "override", value: 1, targetID: 1, targetRole: common.RoleRootUser, operatorRole: common.RoleAdminUser, reason: "permission_denied"},
		{name: "read_error", mode: "add", value: 1, targetID: 1, failRead: true, reason: "database_error"},
		{name: "add_overflow", mode: "add", before: common.MaxWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "subtract_underflow", mode: "subtract", before: -common.MaxWalletQuota, value: 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_add", mode: "add", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_subtract", mode: "subtract", value: common.MaxWalletQuota + 1, targetID: 1, reason: "quota_limit_exceeded"},
		{name: "oversized_override", mode: "override", value: -common.MaxWalletQuota - 1, targetID: 1, reason: "quota_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			createQuotaTestOperator(t, db, tc.operatorRole)
			user := model.User{Id: 1, Username: "private-target-name", Role: tc.targetRole, Quota: tc.before}
			require.NoError(t, db.Create(&user).Error)
			if tc.deleted {
				require.NoError(t, db.Delete(&user).Error)
			}
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:quota_read_error", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" && len(tx.Statement.Selects) == 0 {
						tx.AddError(errors.New("private database error"))
					}
				}))
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, tc.targetID, tc.mode, tc.value)))
			role := tc.operatorRole
			if role == 0 {
				role = common.RoleRootUser
			}
			c.Set("id", 9999)
			c.Set("role", role)
			ManageUser(c)
			require.Contains(t, recorder.Body.String(), `"success":false`)
			if tc.failRead {
				require.NoError(t, db.Callback().Query().Remove("test:quota_read_error"))
			}
			require.NoError(t, db.Unscoped().First(&user, user.Id).Error)
			assert.Equal(t, tc.before, user.Quota)
			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Find(&audits).Error)
			require.Len(t, audits, 1)
			assert.False(t, audits[0].Success)
			params, err := common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			expected, err := common.Marshal(model.AuditFields{"target_user_id": tc.targetID, "mode": tc.mode, "requested_quota": tc.value, "failure_reason": tc.reason})
			require.NoError(t, err)
			assert.JSONEq(t, string(expected), string(params))
			assert.NotContains(t, audits[0].Content, user.Username)
			var count int64
			require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestManageUserQuotaMiddlewareKeepsOneOperationPerRequest(t *testing.T) {
	db := setupManageUserTestDB(t)
	pat := "quota-middleware-test-token"
	operator := model.User{Id: 9999, Username: "root-operator", Role: common.RoleRootUser, Status: common.UserStatusEnabled, AuthVersion: 1, AccessToken: &pat, Quota: 1000}
	require.NoError(t, db.Create(&operator).Error)
	router := gin.New()
	router.Use(middleware.RequestId(), middleware.AccessTokenAudit())
	router.POST("/api/user/manage", middleware.AdminAuth(), ManageUser)
	for _, tc := range []struct {
		body, action string
		success      bool
	}{
		{`{"id":9999,"action":"add_quota","mode":"add","value":100}`, "user.quota_add", true},
		{`{"id":9999,"action":"add_quota","mode":"subtract","value":0}`, "user.quota_subtract", false},
		{`{"id":9999,"action":"add_quota","mode":"invalid","value":1}`, "generic", false},
		{`{"id":`, "generic", false},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(tc.body))
		request.Header.Set("Authorization", "Bearer "+pat)
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, tc.success))
		requestID := recorder.Header().Get(common.RequestIdKey)
		require.NotEmpty(t, requestID)
		var audits []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("request_id = ?", requestID).Find(&audits).Error)
		require.Len(t, audits, 2, "one operation audit and one PAT request audit")
		for _, audit := range audits {
			assert.Equal(t, tc.success, audit.Success)
			if audit.Category != model.AuditCategoryOperation {
				continue
			}
			assert.Equal(t, tc.action, audit.Action)
			assert.Equal(t, operator.Id, audit.UserId)
			assert.Equal(t, "/api/user/manage", audit.Route)
			if tc.success {
				params, err := common.Marshal(audit.Other.Op.Params)
				require.NoError(t, err)
				assert.Contains(t, string(params), `"target_user_id":9999`)
				assert.Contains(t, string(params), `"target_username":"root-operator"`)
			}
		}
		var count int64
		require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", requestID).Count(&count).Error)
		if tc.success {
			assert.EqualValues(t, 1, count)
		} else {
			assert.Zero(t, count)
		}
	}
	require.NoError(t, db.First(&operator, operator.Id).Error)
	assert.Equal(t, 1100, operator.Quota)
}

func TestManageUserQuotaConcurrentSnapshots(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrent-quota", Quota: 1000}
	require.NoError(t, db.Create(&user).Error)
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:concurrent_quota_start", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			ready.Done()
			<-release
		}
	}))
	type result struct {
		adjustment *model.UserQuotaAdjustment
		err        error
		value      int
	}
	results := make(chan result, 2)
	for _, value := range []int{10, 20} {
		go func(value int) {
			adjustment, err := model.AdjustUserQuota(user.Id, common.RoleRootUser, "add", value)
			results <- result{adjustment, err, value}
		}(value)
	}
	ready.Wait()
	close(release)
	var committed []model.UserQuotaAdjustment
	for range 2 {
		result := <-results
		if result.err != nil {
			require.True(t, common.UsingMainDatabase(common.DatabaseTypeSQLite), "row-locking databases must serialize both adjustments: %v", result.err)
			assert.Contains(t, strings.ToLower(result.err.Error()), "locked")
			assert.Nil(t, result.adjustment)
			continue
		}
		require.NotNil(t, result.adjustment)
		assert.Equal(t, result.value, result.adjustment.After-result.adjustment.Before)
		committed = append(committed, *result.adjustment)
	}
	require.NoError(t, db.Callback().Query().Remove("test:concurrent_quota_start"))
	require.NotEmpty(t, committed)
	sort.Slice(committed, func(i, j int) bool { return committed[i].Before < committed[j].Before })
	balance := 1000
	for _, adjustment := range committed {
		assert.Equal(t, balance, adjustment.Before)
		balance = adjustment.After
	}
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, balance, user.Quota)
}

func TestManageUserQuotaCacheUsesCommittedIntegerDifference(t *testing.T) {
	for _, tc := range []struct {
		name, mode                               string
		before, cached, value, after, wantCached int
		failUpdate, failCache, missingCache      bool
	}{
		{name: "add_preserves_reservations", mode: "add", before: 1000, cached: 900, value: 500, after: 1500, wantCached: 1400},
		{name: "subtract_preserves_reservations", mode: "subtract", before: 1000, cached: 900, value: 500, after: 500, wantCached: 400},
		{name: "override_preserves_reservations", mode: "override", before: 1000, cached: 900, value: 2000, after: 2000, wantCached: 1900},
		{name: "large_odd_difference", mode: "override", before: common.MaxWalletQuota - 1, cached: common.MaxWalletQuota - 1, value: -common.MaxWalletQuota, after: -common.MaxWalletQuota, wantCached: -common.MaxWalletQuota},
		{name: "rollback_does_not_change_cache", mode: "subtract", before: 1000, cached: 900, value: 500, after: 1000, wantCached: 900, failUpdate: true},
		{name: "cache_error_keeps_committed_change", mode: "add", before: 1000, value: 500, after: 1500, failCache: true},
		{name: "missing_cache_is_not_partially_created", mode: "add", before: 1000, value: 500, after: 1500, missingCache: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			operator := createQuotaTestOperator(t, db, common.RoleRootUser)
			server := miniredis.RunT(t)
			oldRDB := common.RDB
			common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			common.RedisEnabled = true
			t.Cleanup(func() { _ = common.RDB.Close(); common.RDB = oldRDB })
			user := model.User{Username: "cached-quota", Quota: tc.before, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			cache, err := model.GetUserCache(user.Id)
			require.NoError(t, err)
			assert.Equal(t, tc.before, cache.Quota)
			_, err = model.GetUserCache(operator.Id)
			require.NoError(t, err)
			keys := server.Keys()
			var quotaKey string
			for _, key := range keys {
				if server.HGet(key, "Id") == strconv.Itoa(user.Id) && server.HGet(key, "Quota") != "" {
					quotaKey = key
					break
				}
			}
			require.NotEmpty(t, quotaKey)
			server.HSet(quotaKey, "Quota", strconv.Itoa(tc.cached))
			if tc.missingCache {
				server.Del(quotaKey)
			}
			if tc.failCache {
				server.SetError("ERR quota cache unavailable")
			}
			if tc.failUpdate {
				require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:cache_quota_rollback", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("quota rollback"))
					}
				}))
			}
			recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":%q,"value":%d}`, user.Id, tc.mode, tc.value))
			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, !tc.failUpdate))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.after, user.Quota)
			if tc.missingCache {
				// Log username lookup may hydrate the whole user after commit.
				if server.Exists(quotaKey) {
					assert.Equal(t, strconv.Itoa(user.Id), server.HGet(quotaKey, "Id"))
					assert.NotEmpty(t, server.HGet(quotaKey, "CacheSchema"))
					assert.Equal(t, strconv.Itoa(tc.after), server.HGet(quotaKey, "Quota"))
				}
			} else if !tc.failCache {
				assert.Equal(t, strconv.Itoa(tc.wantCached), server.HGet(quotaKey, "Quota"))
			}
		})
	}
}

// Saving notification settings starts from what the user stored: fields the
// form does not manage stay, and switches an older client leaves out keep
// their stored values. Only administrators change the channel and upstream
// switches, and a threshold of 0 means the site default.
func TestUpdateUserSettingKeepsWhatTheRequestLeavesOut(t *testing.T) {
	on, off := true, false
	stored := dto.UserSetting{
		NotifyType:                       dto.NotifyTypeBark,
		BarkUrl:                          "https://api.day.app/key/{{title}}/{{content}}",
		QuotaWarningThreshold:            500000,
		QuotaWarningEnabled:              &off,
		ChannelNotifyEnabled:             &on,
		UpstreamModelUpdateNotifyEnabled: true,
		Language:                         "zhCN",
		SidebarModules:                   `{"chat":{"enabled":true}}`,
		BillingPreference:                "wallet_only",
	}
	kept := func(setting dto.UserSetting) dto.UserSetting {
		setting.Language, setting.SidebarModules, setting.BillingPreference = stored.Language, stored.SidebarModules, stored.BillingPreference
		return setting
	}
	switchesChanged := `{"notify_type":"email","quota_warning_threshold":0,"quota_warning_enabled":true,"channel_notify_enabled":false,"upstream_model_update_notify_enabled":false}`
	for _, tc := range []struct {
		name        string
		role        int
		body        string
		wantSuccess bool
		want        dto.UserSetting
	}{
		{
			name:        "older client without the new switches",
			role:        common.RoleAdminUser,
			body:        `{"notify_type":"webhook","quota_warning_threshold":2000,"webhook_url":"https://example.com/hook","webhook_secret":"hook-secret","accept_unset_model_ratio_model":true,"record_ip_log":true}`,
			wantSuccess: true,
			want: kept(dto.UserSetting{
				NotifyType: dto.NotifyTypeWebhook, WebhookUrl: "https://example.com/hook", WebhookSecret: "hook-secret",
				QuotaWarningThreshold: 2000, QuotaWarningEnabled: &off, ChannelNotifyEnabled: &on, UpstreamModelUpdateNotifyEnabled: true,
				AcceptUnsetRatioModel: true, RecordIpLog: true,
			}),
		},
		{
			name:        "administrator changes the switches and uses the site default threshold",
			role:        common.RoleAdminUser,
			body:        switchesChanged,
			wantSuccess: true,
			want:        kept(dto.UserSetting{NotifyType: dto.NotifyTypeEmail, QuotaWarningEnabled: &on, ChannelNotifyEnabled: &off}),
		},
		{
			name:        "user cannot change the administrator switches",
			role:        common.RoleCommonUser,
			body:        switchesChanged,
			wantSuccess: true,
			want:        kept(dto.UserSetting{NotifyType: dto.NotifyTypeEmail, QuotaWarningEnabled: &on, ChannelNotifyEnabled: &on, UpstreamModelUpdateNotifyEnabled: true}),
		},
		{
			name: "negative threshold",
			role: common.RoleCommonUser,
			body: `{"notify_type":"email","quota_warning_threshold":-1}`,
			want: stored,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			setting, err := common.Marshal(stored)
			require.NoError(t, err)
			user := model.User{Username: "setting-owner", Role: tc.role, Status: common.UserStatusEnabled, AffCode: "setting-owner-aff", Setting: string(setting)}
			require.NoError(t, db.Create(&user).Error)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/user/setting", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("id", user.Id)
			UpdateUserSetting(c)

			assert.Contains(t, recorder.Body.String(), fmt.Sprintf(`"success":%t`, tc.wantSuccess))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, tc.want, user.GetSetting())
		})
	}
}

func TestUserConcurrencyNewAccountDefaults(t *testing.T) {
	for _, tc := range []struct {
		name        string
		password    string
		transaction bool
		limit       int
		want        int
	}{
		{name: "password account", password: "new-account-password", want: 5},
		{name: "passwordless account", transaction: true, want: 5},
		{name: "explicit limit", password: "new-account-password", limit: 7, want: 7},
		{name: "explicit transaction limit", transaction: true, limit: 7, want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			existing := model.User{Username: "existing-unlimited", Password: "existing-hash", AffCode: "existing-aff"}
			require.NoError(t, db.Create(&existing).Error)
			user := model.User{Username: "new-account", Password: tc.password, MaxConcurrentRequests: tc.limit}
			if tc.transaction {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return user.InsertWithTx(tx, 0) }))
			} else {
				require.NoError(t, user.Insert(0))
			}
			var stored model.User
			require.NoError(t, db.First(&stored, user.Id).Error)
			assert.Equal(t, tc.want, stored.MaxConcurrentRequests)
			if tc.password == "" {
				assert.Empty(t, stored.Password)
			} else {
				assert.True(t, common.ValidatePasswordAndHash(tc.password, stored.Password))
			}
			require.NoError(t, db.First(&existing, existing.Id).Error)
			assert.Zero(t, existing.MaxConcurrentRequests)
		})
	}
}

func TestUserConcurrencyNewAccountHandlers(t *testing.T) {
	previousRegister, previousPasswordRegister := common.RegisterEnabled, common.PasswordRegisterEnabled
	previousEmailVerification, previousDefaultToken := common.EmailVerificationEnabled, constant.GenerateDefaultToken
	common.RegisterEnabled, common.PasswordRegisterEnabled = true, true
	common.EmailVerificationEnabled, constant.GenerateDefaultToken = false, false
	t.Cleanup(func() {
		common.RegisterEnabled, common.PasswordRegisterEnabled = previousRegister, previousPasswordRegister
		common.EmailVerificationEnabled, constant.GenerateDefaultToken = previousEmailVerification, previousDefaultToken
	})
	for _, tc := range []struct {
		name    string
		handler gin.HandlerFunc
		role    int
	}{
		{name: "register", handler: Register},
		{name: "admin create", handler: CreateUser, role: common.RoleAdminUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			for i, suppliedLimit := range []string{"", `,"max_concurrent_requests":0`, `,"max_concurrent_requests":99`} {
				username := fmt.Sprintf("new-cap-%d", i)
				body := fmt.Sprintf(`{"username":%q,"password":"new-account-password","role":1%s}`, username, suppliedLimit)
				recorder := adminUserRequest(http.MethodPost, "/api/user/", body, "", service.AuthIdentity{}, tc.role, nil, tc.handler)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Contains(t, recorder.Body.String(), `"success":true`)
				var user model.User
				require.NoError(t, db.Where("username = ?", username).First(&user).Error)
				assert.Equal(t, 5, user.MaxConcurrentRequests)
			}
		})
	}
}

func TestUserConcurrencySetupAccountDefault(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Setup{}))
	previousSetup := constant.Setup
	previousSelfUse, previousDemo := operation_setting.SelfUseModeEnabled, operation_setting.DemoSiteEnabled
	common.OptionMapRWMutex.Lock()
	previousOptions := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	constant.Setup = false
	t.Cleanup(func() {
		constant.Setup = previousSetup
		operation_setting.SelfUseModeEnabled, operation_setting.DemoSiteEnabled = previousSelfUse, previousDemo
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptions
		common.OptionMapRWMutex.Unlock()
	})

	recorder := adminUserRequest(http.MethodPost, "/api/setup", `{"username":"initial-root","password":"new-account-password","confirmPassword":"new-account-password"}`, "", service.AuthIdentity{}, 0, nil, PostSetup)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	var user model.User
	require.NoError(t, db.Where("username = ?", "initial-root").First(&user).Error)
	assert.Equal(t, 5, user.MaxConcurrentRequests)
	assert.Equal(t, common.RoleRootUser, user.Role)
}

func TestUserConcurrencyEndpointsAndPersistence(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}))
	user := model.User{Username: "concurrency-user", AffCode: "concurrency-user", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default"}
	admin := model.User{Username: "concurrency-admin", AffCode: "concurrency-admin", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&admin).Error)
	own := model.Token{UserId: user.Id, Key: strings.Repeat("a", 48), Name: "own"}
	other := model.Token{UserId: admin.Id, Key: strings.Repeat("b", 48), Name: "other"}
	require.NoError(t, db.Create(&own).Error)
	require.NoError(t, db.Create(&other).Error)
	request := func(method, path, body string, role int, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", user.Id)
		c.Set("role", role)
		c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(user.Id)}}
		handler(c)
		return r
	}
	for _, body := range []string{`{}`, `{"max_concurrent_requests":-1}`, `{"max_concurrent_requests":1.5}`, `{"max_concurrent_requests":2147483648}`} {
		response := request(http.MethodPut, "/", body, common.RoleRootUser, UpdateUserConcurrency)
		assert.Contains(t, response.Body.String(), `"success":false`)
	}
	response := request(http.MethodPut, "/", `{"max_concurrent_requests":3}`, common.RoleCommonUser, UpdateUserConcurrency)
	assert.Contains(t, response.Body.String(), `"success":false`)
	response = request(http.MethodPut, "/", `{"max_concurrent_requests":3}`, common.RoleAdminUser, UpdateUserConcurrency)
	require.Contains(t, response.Body.String(), `"success":true`)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 3, user.MaxConcurrentRequests)
	// A stale profile snapshot may not overwrite the administrator's cap.
	stale := model.User{Id: user.Id, DisplayName: "new name", MaxConcurrentRequests: 99}
	require.NoError(t, stale.Update(false))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 3, user.MaxConcurrentRequests)
	lease, err := service.AcquireRelayConcurrency(context.Background(), user.Id, own.Id, 3)
	require.NoError(t, err)
	defer lease.Release(context.Background())
	response = request(http.MethodGet, fmt.Sprintf("/?ids=%d", own.Id), "", common.RoleCommonUser, GetTokenConcurrency)
	assert.Contains(t, response.Body.String(), `"current":1`)
	assert.Contains(t, response.Body.String(), `"max_concurrent_requests":3`)
	assert.Contains(t, response.Body.String(), fmt.Sprintf(`"tokens":{"%d":1}`, own.Id))
	assert.NotContains(t, response.Body.String(), own.Key)
	response = request(http.MethodGet, fmt.Sprintf("/?ids=%d", other.Id), "", common.RoleCommonUser, GetTokenConcurrency)
	assert.Contains(t, response.Body.String(), `"success":false`)
	response = request(http.MethodGet, fmt.Sprintf("/?ids=%d", user.Id), "", common.RoleCommonUser, GetUserConcurrency)
	assert.Contains(t, response.Body.String(), `"success":false`)
	response = request(http.MethodGet, fmt.Sprintf("/?ids=%d", user.Id), "", common.RoleAdminUser, GetUserConcurrency)
	assert.Contains(t, response.Body.String(), `"success":true`)
	assert.Contains(t, response.Body.String(), `"current":1`)
	// Administrators cannot edit peer administrators, even by direct handler access.
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"max_concurrent_requests":8}`))
	c.Set("role", common.RoleAdminUser)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(admin.Id)}}
	UpdateUserConcurrency(c)
	assert.Contains(t, r.Body.String(), `"success":false`)
	response = request(http.MethodPut, "/", `{"max_concurrent_requests":0}`, common.RoleRootUser, UpdateUserConcurrency)
	require.Contains(t, response.Body.String(), `"success":true`)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Zero(t, user.MaxConcurrentRequests)
	// A saved limit remains a successful mutation when the live snapshot fails.
	oldRedis, oldEnabled := common.RDB, common.RedisEnabled
	defer func() { common.RDB, common.RedisEnabled = oldRedis, oldEnabled }()
	server := miniredis.RunT(t)
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	defer common.RDB.Close()
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	r = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(r)
	c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"max_concurrent_requests":4}`)).WithContext(canceled)
	c.Set("id", user.Id)
	c.Set("role", common.RoleRootUser)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(user.Id)}}
	UpdateUserConcurrency(c)
	assert.Equal(t, http.StatusOK, r.Code)
	assert.Contains(t, r.Body.String(), `"success":true`)
	assert.Contains(t, r.Body.String(), `"current":null`)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 4, user.MaxConcurrentRequests)
}

// releasedConcurrencyUser preserves the users schema from v1.0.0-rc.41.
// Verified with git diff v1.0.0-rc.41 HEAD -- model/user.go (no differences)
// before adding MaxConcurrentRequests; keep this fixture frozen for upgrades.
type releasedConcurrencyUser struct {
	Id                   int                        `json:"id"`
	Username             string                     `json:"username" gorm:"unique;index" validate:"max=20"`
	Password             string                     `json:"password" gorm:"not null;" validate:"min=8,max=128"`
	HasPassword          bool                       `json:"-" gorm:"-:all"`
	OriginalPassword     string                     `json:"original_password" gorm:"-:all"`
	DisplayName          string                     `json:"display_name" gorm:"index" validate:"max=20"`
	Role                 int                        `json:"role" gorm:"type:int;default:1"`
	Status               int                        `json:"status" gorm:"type:int;default:1"`
	Email                string                     `json:"email" gorm:"index" validate:"max=50"`
	GitHubId             string                     `json:"github_id" gorm:"column:github_id;index"`
	DiscordId            string                     `json:"discord_id" gorm:"column:discord_id;index"`
	OidcId               string                     `json:"oidc_id" gorm:"column:oidc_id;index"`
	WeChatId             string                     `json:"wechat_id" gorm:"column:wechat_id;index"`
	TelegramId           string                     `json:"telegram_id" gorm:"column:telegram_id;index"`
	VerificationCode     string                     `json:"verification_code" gorm:"-:all"`
	AccessToken          *string                    `json:"-" gorm:"type:char(32);column:access_token;uniqueIndex"`
	AccessTokenCreatedAt *int64                     `json:"-" gorm:"type:bigint;column:access_token_created_at"`
	Quota                int                        `json:"quota" gorm:"type:int;default:0"`
	UsedQuota            int                        `json:"used_quota" gorm:"type:int;default:0;column:used_quota"`
	RequestCount         int                        `json:"request_count" gorm:"type:int;default:0;"`
	Group                string                     `json:"group" gorm:"type:varchar(64);default:'default'"`
	AffCode              string                     `json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount             int                        `json:"aff_count" gorm:"type:int;default:0;column:aff_count"`
	AffQuota             int                        `json:"aff_quota" gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota      int                        `json:"aff_history_quota" gorm:"type:int;default:0;column:aff_history"`
	InviterId            int                        `json:"inviter_id" gorm:"type:int;column:inviter_id;index"`
	DeletedAt            gorm.DeletedAt             `gorm:"index"`
	LinuxDOId            string                     `json:"linux_do_id" gorm:"column:linux_do_id;index"`
	Setting              string                     `json:"setting" gorm:"type:text;column:setting"`
	Remark               string                     `json:"remark,omitempty" gorm:"type:varchar(255)" validate:"max=255"`
	StripeCustomer       string                     `json:"stripe_customer" gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt            int64                      `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	LastLoginAt          int64                      `json:"last_login_at" gorm:"default:0;column:last_login_at"`
	AuthVersion          int64                      `json:"-" gorm:"type:bigint;not null;default:1;column:auth_version"`
	AdminPermissions     map[string]map[string]bool `json:"admin_permissions,omitempty" gorm:"-:all"`
}

func (releasedConcurrencyUser) TableName() string { return "users" }

func TestUserConcurrencyColumnMigration(t *testing.T) {
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	db, _ := newAuditTestDatabase(t, dialect, os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN"))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("released-schema upgrade database: %s %s", dialect, version)
	// Build the released schema in an empty isolated database before any current
	// User migration. Existing rows and unique indexes must survive the upgrade.
	require.NoError(t, db.AutoMigrate(&releasedConcurrencyUser{}))
	user := releasedConcurrencyUser{Username: "concurrency-upgrade", Password: "preserve-password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Setting: `{"language":"zh"}`}
	require.NoError(t, db.Create(&user).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.User{}))
	}
	var upgraded model.User
	require.NoError(t, db.First(&upgraded, user.Id).Error)
	assert.Zero(t, upgraded.MaxConcurrentRequests)
	assert.Equal(t, user.Password, upgraded.Password)
	assert.Equal(t, user.Setting, upgraded.Setting)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Update("max_concurrent_requests", 7).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.User{}))
	}
	require.NoError(t, db.First(&upgraded, user.Id).Error)
	assert.Equal(t, 7, upgraded.MaxConcurrentRequests)
	duplicate := model.User{Username: user.Username, AffCode: "unique-duplicate-code", Password: "password"}
	assert.Error(t, db.Create(&duplicate).Error)
}

func TestRelayConcurrencyMiddlewareLifetime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrency-stream", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", MaxConcurrentRequests: 1}
	require.NoError(t, db.Create(&user).Error)
	entered, finish := make(chan struct{}), make(chan struct{})
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(func(c *gin.Context) {
		c.Set("id", user.Id)
		token, _ := strconv.Atoi(c.GetHeader("X-Test-Token"))
		c.Set("token_id", token)
	})
	engine.Use(middleware.RelayConcurrency())
	engine.GET("/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()
		close(entered)
		<-finish
	})
	engine.GET("/error", func(c *gin.Context) { c.AbortWithStatus(http.StatusBadGateway) })
	engine.GET("/panic", func(c *gin.Context) { panic("concurrency regression") })
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		r := httptest.NewRequest(http.MethodGet, "/stream", nil)
		r.Header.Set("X-Test-Token", "1")
		engine.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-entered
	snapshot, err := service.GetRelayConcurrency(context.Background(), user.Id, []int{1, 2})
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.Current)
	assert.Equal(t, 1, snapshot.Tokens[1])
	denied := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/error", nil)
	r.Header.Set("X-Test-Token", "2")
	engine.ServeHTTP(denied, r)
	assert.Equal(t, http.StatusTooManyRequests, denied.Code)
	close(finish)
	<-firstDone
	for _, path := range []string{"/error", "/panic"} {
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		snapshot, err = service.GetRelayConcurrency(context.Background(), user.Id, nil)
		require.NoError(t, err)
		assert.Zero(t, snapshot.Current)
	}
}

func TestRelayConcurrencyWebSocketAndCancellation(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrency-ws", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", MaxConcurrentRequests: 1}
	require.NoError(t, db.Create(&user).Error)
	engine := gin.New()
	done := make(chan struct{}, 2)
	engine.Use(func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		c.Set("id", user.Id)
		c.Set("token_id", 9)
		c.Next()
	}, middleware.RelayConcurrency())
	engine.GET("/ws", func(c *gin.Context) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
	})
	entered := make(chan struct{})
	engine.GET("/cancel", func(c *gin.Context) { close(entered); <-c.Request.Context().Done() })
	server := httptest.NewServer(engine)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	snapshot, err := service.GetRelayConcurrency(context.Background(), user.Id, []int{9})
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.Current)
	assert.Equal(t, 1, snapshot.Tokens[9])
	require.NoError(t, conn.Close())
	<-done
	snapshot, err = service.GetRelayConcurrency(context.Background(), user.Id, nil)
	require.NoError(t, err)
	assert.Zero(t, snapshot.Current)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/cancel", nil).WithContext(ctx)
	go engine.ServeHTTP(httptest.NewRecorder(), req)
	<-entered
	cancel()
	<-done
	snapshot, err = service.GetRelayConcurrency(context.Background(), user.Id, nil)
	require.NoError(t, err)
	assert.Zero(t, snapshot.Current)
	oldRedis, oldEnabled := common.RDB, common.RedisEnabled
	defer func() { common.RDB, common.RedisEnabled = oldRedis, oldEnabled }()
	common.RedisEnabled = true
	common.RDB = nil
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ws", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

// stalledConcurrencyConn models an upgraded connection whose shutdown has not
// completed. The request's slot must remain reserved until shutdown finishes.
type stalledConcurrencyConn struct {
	net.Conn
	closeStarted chan struct{}
	allowClose   chan struct{}
	closeOnce    sync.Once
}

func (connection *stalledConcurrencyConn) Close() error {
	connection.closeOnce.Do(func() { close(connection.closeStarted) })
	<-connection.allowClose
	return connection.Conn.Close()
}

type concurrencyHijackRecorder struct {
	*httptest.ResponseRecorder
	connection net.Conn
}

func (recorder *concurrencyHijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return recorder.connection, bufio.NewReadWriter(bufio.NewReader(recorder.connection), bufio.NewWriter(recorder.connection)), nil
}

func TestRelayConcurrencyCancellationWaitsForConnectionShutdown(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{Username: "concurrency-shutdown", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", MaxConcurrentRequests: 1}
	require.NoError(t, db.Create(&user).Error)
	synctest.Test(t, func(t *testing.T) {
		connection, peer := net.Pipe()
		defer peer.Close()
		stalled := &stalledConcurrencyConn{Conn: connection, closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
		entered, handlerFinishing, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", user.Id); c.Set("token_id", 9) }, middleware.RelayConcurrency())
		engine.GET("/", func(c *gin.Context) {
			_, _, err := c.Writer.Hijack()
			if err != nil {
				panic(err)
			}
			close(entered)
			<-c.Request.Context().Done()
			<-stalled.closeStarted
			close(handlerFinishing)
		})
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		go func() {
			defer close(finished)
			engine.ServeHTTP(&concurrencyHijackRecorder{ResponseRecorder: httptest.NewRecorder(), connection: stalled}, request)
		}()
		<-entered
		cancel()
		<-handlerFinishing
		synctest.Wait()
		snapshot, err := service.GetRelayConcurrency(context.Background(), user.Id, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, snapshot.Current, "slot stays reserved while the watcher finishes closing the connection")
		select {
		case <-finished:
			t.Error("request cleanup returned before connection shutdown")
		default:
		}
		close(stalled.allowClose)
		<-finished
		snapshot, err = service.GetRelayConcurrency(context.Background(), user.Id, nil)
		require.NoError(t, err)
		assert.Zero(t, snapshot.Current)
	})
}
