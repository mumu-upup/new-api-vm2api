package service

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// External DSNs must point to isolated test databases; these fixtures own their data.
func setupResponseModelMismatchTables(t *testing.T) {
	t.Helper()
	dialect := common.DatabaseType(os.Getenv("MODEL_MONITOR_TEST_DIALECT"))
	if dialect == "" {
		dialect = common.DatabaseTypeSQLite
	}
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
	})
	for _, target := range []struct {
		env string
		db  **gorm.DB
	}{
		{"MODEL_MONITOR_TEST_DSN", &model.DB},
		{"MODEL_MONITOR_TEST_LOG_DSN", &model.LOG_DB},
	} {
		var driver gorm.Dialector = sqlite.Open(":memory:")
		if dialect != common.DatabaseTypeSQLite {
			dsn := os.Getenv(target.env)
			require.NotEmpty(t, dsn, "an isolated test database is required: %s", target.env)
			switch dialect {
			case common.DatabaseTypeMySQL:
				driver = mysql.Open(dsn)
			case common.DatabaseTypePostgreSQL:
				driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
			default:
				t.Fatalf("unsupported test dialect: %s", dialect)
			}
		}
		db, err := gorm.Open(driver, &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		*target.db = db
	}
	common.SetDatabaseTypes(dialect, dialect)
	require.NoError(t, model.DB.AutoMigrate(&model.ResponseModelMismatch{}, &model.Channel{}, &model.User{}))
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	versionQuery := "SELECT version()"
	if dialect == common.DatabaseTypeSQLite {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, model.DB.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s; separate log database", dialect, version)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ResponseModelMismatch{}).Error)
		require.NoError(t, model.DB.Where("1 = 1").Delete(&model.Channel{}).Error)
		require.NoError(t, model.DB.Unscoped().Where("1 = 1").Delete(&model.User{}).Error)
		require.NoError(t, model.LOG_DB.Where("1 = 1").Delete(&model.Log{}).Error)
	})
}

func TestRecordResponseModelMismatchStoresOnlySubstitutedModels(t *testing.T) {
	setupResponseModelMismatchTables(t)
	gin.SetMode(gin.TestMode)
	require.NoError(t, model.DB.Create(&model.User{Id: 7, Username: "alice"}).Error)

	tests := []struct {
		name        string
		observation *relaycommon.ResponseModel
		stored      bool
		actual      string
	}{
		{name: "no observation", observation: nil},
		{name: "matching model", observation: &relaycommon.ResponseModel{RequestedModel: "gpt-5", ReturnedModel: "gpt-5"}},
		{name: "missing returned model", observation: &relaycommon.ResponseModel{RequestedModel: "gpt-5"}},
		{name: "matching model with degradation flag", observation: &relaycommon.ResponseModel{RequestedModel: "gpt-5", ReturnedModel: "gpt-5", ActualModel: "gpt-5", Degraded: common.GetPointer(true)}},
		{name: "degradation flag without model", observation: &relaycommon.ResponseModel{RequestedModel: "gpt-5", Degraded: common.GetPointer(true)}},
		{name: "dated variant", observation: &relaycommon.ResponseModel{RequestedModel: "claude-sonnet-4", ReturnedModel: "claude-sonnet-4-20250514"}},
		{name: "provider path", observation: &relaycommon.ResponseModel{RequestedModel: "deepseek-v4", ReturnedModel: "deepseek/deepseek-v4"}},
		{name: "case only", observation: &relaycommon.ResponseModel{RequestedModel: "GPT-5", ReturnedModel: "gpt-5"}},
		{name: "matches mapped upstream", observation: &relaycommon.ResponseModel{RequestedModel: "smart", UpstreamModel: "gpt-5", ReturnedModel: "gpt-5-2026-01-01"}},
		{name: "substituted model", observation: &relaycommon.ResponseModel{RequestedModel: "claude-opus-4", UpstreamModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku"}, stored: true, actual: "claude-3-5-haiku"},
		{name: "actual model differs despite matching returned model", observation: &relaycommon.ResponseModel{RequestedModel: "claude-opus-4", UpstreamModel: "claude-opus-4", ReturnedModel: "claude-opus-4", ActualModel: "claude-3-5-haiku"}, stored: true, actual: "claude-3-5-haiku"},
		{name: "returned mismatch and explicit degradation", observation: &relaycommon.ResponseModel{RequestedModel: "claude-opus-4", UpstreamModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku", Degraded: common.GetPointer(true)}, stored: true, actual: "claude-3-5-haiku"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, model.DB.Where("1 = 1").Delete(&model.ResponseModelMismatch{}).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set(common.RequestIdKey, "req-1")
			ctx.Set("username", "alice")
			relayInfo := &relaycommon.RelayInfo{UserId: 7, ResponseModel: tt.observation}

			other := model.NewLogOther()
			other.SetPublic("diagnostic_marker", "preserve")
			AppendResponseModelLogInfo(relayInfo, other)
			params := model.RecordConsumeLogParams{
				ChannelId:        3,
				ModelName:        "claude-opus-4",
				TokenName:        "default",
				Quota:            1200,
				PromptTokens:     100,
				CompletionTokens: 20,
				UseTimeSeconds:   4,
				IsStream:         true,
				Group:            "vip",
				Other:            other,
			}
			RecordResponseModelMismatch(ctx, relayInfo, params)
			model.RecordConsumeLog(ctx, relayInfo.UserId, params)
			var log model.Log
			require.NoError(t, model.LOG_DB.Order("id DESC").First(&log).Error)
			assert.Equal(t, 1200, log.Quota)
			assert.Equal(t, 100, log.PromptTokens)
			assert.Equal(t, 20, log.CompletionTokens)
			var fields map[string]any
			require.NoError(t, common.UnmarshalJsonStr(log.Other, &fields))
			assert.Equal(t, "preserve", fields["diagnostic_marker"])
			_, recorded := fields["response_model"]
			assert.Equal(t, tt.stored, recorded)

			var records []model.ResponseModelMismatch
			require.NoError(t, model.DB.Find(&records).Error)
			if !tt.stored {
				assert.Empty(t, records)
				return
			}
			require.Len(t, records, 1)
			record := records[0]
			assert.NotZero(t, record.CreatedAt)
			assert.Equal(t, "req-1", record.RequestId)
			assert.Equal(t, 7, record.UserId)
			assert.Equal(t, "alice", record.Username)
			assert.Equal(t, "default", record.TokenName)
			assert.Equal(t, 3, record.ChannelId)
			assert.Equal(t, "vip", record.Group)
			assert.Equal(t, "claude-opus-4", record.RequestedModel)
			assert.Equal(t, "claude-opus-4", record.UpstreamModel)
			assert.Equal(t, tt.actual, record.ReturnedModel)
			assert.Equal(t, 1200, record.Quota)
			assert.Equal(t, 100, record.PromptTokens)
			assert.Equal(t, 20, record.CompletionTokens)
			assert.Equal(t, 4, record.UseTime)
			assert.True(t, record.IsStream)
		})
	}
}

func TestGetResponseModelMismatchStatsAggregatesRecordedMismatches(t *testing.T) {
	setupResponseModelMismatchTables(t)
	const start, end = int64(1_790_000_000 - 1_790_000_000%3600), int64(1_790_000_000 - 1_790_000_000%3600 + 7200)

	require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Name: "relay-a", Key: "sk-a", Status: common.ChannelStatusEnabled}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 2, Name: "relay-b", Key: "sk-b", Status: common.ChannelStatusEnabled}).Error)
	consumeLogs := []model.Log{
		{Type: model.LogTypeConsume, ChannelId: 1, CreatedAt: start + 10},
		{Type: model.LogTypeConsume, ChannelId: 1, CreatedAt: start + 20},
		{Type: model.LogTypeConsume, ChannelId: 1, CreatedAt: start + 3700},
		{Type: model.LogTypeConsume, ChannelId: 1, CreatedAt: start + 3800},
		{Type: model.LogTypeConsume, ChannelId: 2, CreatedAt: start + 30},
		{Type: model.LogTypeConsume, ChannelId: 2, CreatedAt: start + 40},
		{Type: model.LogTypeError, ChannelId: 2, CreatedAt: start + 50},
		{Type: model.LogTypeConsume, ChannelId: 2, CreatedAt: end + 1},
	}
	require.NoError(t, model.LOG_DB.Create(&consumeLogs).Error)
	records := []model.ResponseModelMismatch{
		{CreatedAt: start + 10, UserId: 1, ChannelId: 1, RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku", Quota: 100, RequestId: "a1"},
		{CreatedAt: start + 3700, UserId: 2, ChannelId: 1, RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku", Quota: 200, RequestId: "a2"},
		{CreatedAt: start + 3800, UserId: 1, ChannelId: 1, RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-haiku", Quota: 300, RequestId: "a3"},
		{CreatedAt: start + 30, UserId: 3, ChannelId: 2, RequestedModel: "gpt-5", ReturnedModel: "gpt-4.1", Quota: 400, RequestId: "b1"},
		{CreatedAt: start + 40, UserId: 3, ChannelId: 9, RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku", Quota: 500, RequestId: "c1"},
		{CreatedAt: end + 1, UserId: 4, ChannelId: 2, RequestedModel: "gpt-5", ReturnedModel: "gpt-4.1", Quota: 600, RequestId: "late"},
	}
	require.NoError(t, model.DB.Create(&records).Error)

	stats, err := GetResponseModelMismatchStats(start, end)
	require.NoError(t, err)

	assert.Equal(t, int64(6), stats.Summary.TotalRequests)
	assert.Equal(t, int64(5), stats.Summary.MismatchRequests)
	assert.Equal(t, int64(1500), stats.Summary.MismatchQuota)
	assert.Equal(t, int64(3), stats.Summary.AffectedChannels)
	assert.Equal(t, int64(3), stats.Summary.AffectedUsers)

	assert.Equal(t, []ResponseModelMismatchChannel{
		{ChannelId: 1, ChannelName: "relay-a", TotalRequests: 4, MismatchRequests: 3, MismatchQuota: 600, LastSeenAt: start + 3800, ReturnedModels: []string{"claude-3-5-haiku", "claude-3-haiku"}},
		{ChannelId: 2, ChannelName: "relay-b", TotalRequests: 2, MismatchRequests: 1, MismatchQuota: 400, LastSeenAt: start + 30, ReturnedModels: []string{"gpt-4.1"}},
		{ChannelId: 9, ChannelName: "channel-9", TotalRequests: 0, MismatchRequests: 1, MismatchQuota: 500, LastSeenAt: start + 40, ReturnedModels: []string{"claude-3-5-haiku"}},
	}, stats.Channels)
	assert.Equal(t, []ResponseModelMismatchPair{
		{RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-5-haiku", Requests: 3, Channels: 2, LastSeenAt: start + 3700},
		{RequestedModel: "claude-opus-4", ReturnedModel: "claude-3-haiku", Requests: 1, Channels: 1, LastSeenAt: start + 3800},
		{RequestedModel: "gpt-5", ReturnedModel: "gpt-4.1", Requests: 1, Channels: 1, LastSeenAt: start + 30},
	}, stats.Pairs)
	assert.Equal(t, []ResponseModelMismatchBucket{
		{Timestamp: start, Requests: 3},
		{Timestamp: start + 3600, Requests: 2},
	}, stats.Trend)

	requestIds := make([]string, 0, len(stats.Items))
	for _, item := range stats.Items {
		requestIds = append(requestIds, item.RequestId)
	}
	assert.Equal(t, []string{"a3", "a2", "c1", "b1", "a1"}, requestIds)
	assert.Equal(t, "relay-a", stats.Items[0].ChannelName)
	assert.Equal(t, "channel-9", stats.Items[2].ChannelName)
}

func TestPurgePreviousWeekResponseModelMismatchesKeepsCurrentWeek(t *testing.T) {
	setupResponseModelMismatchTables(t)
	shanghai := time.FixedZone("UTC+8", 8*3600)
	monday := time.Date(2026, 9, 28, 0, 0, 0, 0, shanghai)

	weekStarts := []struct {
		name string
		now  time.Time
	}{
		{name: "monday midnight", now: monday},
		{name: "wednesday afternoon", now: time.Date(2026, 9, 30, 15, 4, 5, 0, shanghai)},
		{name: "sunday last second", now: time.Date(2026, 10, 4, 23, 59, 59, 0, shanghai)},
	}
	for _, tt := range weekStarts {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, monday, responseModelMismatchWeekStart(tt.now))
		})
	}

	require.NoError(t, model.DB.Create(&[]model.ResponseModelMismatch{
		{CreatedAt: monday.Add(-7 * 24 * time.Hour).Unix(), RequestId: "last-week-start"},
		{CreatedAt: monday.Unix() - 1, RequestId: "last-week-end"},
		{CreatedAt: monday.Unix(), RequestId: "this-week-start"},
		{CreatedAt: monday.Add(50 * time.Hour).Unix(), RequestId: "this-week"},
	}).Error)

	weekStart, deleted, err := purgePreviousWeekResponseModelMismatches(time.Date(2026, 9, 30, 12, 0, 0, 0, shanghai))
	require.NoError(t, err)
	assert.Equal(t, monday, weekStart)
	assert.Equal(t, int64(2), deleted)

	var remaining []string
	require.NoError(t, model.DB.Model(&model.ResponseModelMismatch{}).Order("created_at").Pluck("request_id", &remaining).Error)
	assert.Equal(t, []string{"this-week-start", "this-week"}, remaining)
}
