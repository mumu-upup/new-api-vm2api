package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRecordConsumeLogRecordsClientIPByDefault(t *testing.T) {
	truncateTables(t)
	common.LogConsumeEnabled = true
	user := User{Id: 101, Username: "ip-default", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(&user).Error)
	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Request.RemoteAddr = "198.51.100.10:1234"

	RecordConsumeLog(c, user.Id, RecordConsumeLogParams{ModelName: "test-model"})

	var log Log
	require.NoError(t, LOG_DB.Where("user_id = ?", user.Id).First(&log).Error)
	require.Equal(t, "198.51.100.10", log.Ip)
}

func TestRecordErrorLogRespectsExplicitIPOptOut(t *testing.T) {
	truncateTables(t)
	user := User{Id: 102, Username: "ip-opt-out", Status: common.UserStatusEnabled, Setting: `{"record_ip_log":false}`}
	require.NoError(t, DB.Create(&user).Error)
	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Request.RemoteAddr = "198.51.100.11:1234"

	RecordErrorLog(c, user.Id, 0, "test-model", "", "error", 0, 1, false, "default", NewLogOther())

	var log Log
	require.NoError(t, LOG_DB.Where("user_id = ?", user.Id).First(&log).Error)
	require.Empty(t, log.Ip)
}
