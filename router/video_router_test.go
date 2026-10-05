package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOpenAIVideoRouteRendersJimengTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousDB := model.DB
	previousDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousSQLitePath := common.SQLitePath
	previousMasterNode := common.IsMasterNode
	previousRedisEnabled := common.RedisEnabled
	common.SQLitePath = t.TempDir() + "/router-video.db"
	common.IsMasterNode = false
	common.RedisEnabled = false
	t.Setenv("SQL_DSN", "")
	require.NoError(t, model.InitDB())
	database := model.DB
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}))
	t.Cleanup(func() {
		sqlDB, closeErr := database.DB()
		require.NoError(t, closeErr)
		require.NoError(t, sqlDB.Close())
		model.DB = previousDB
		common.SetDatabaseTypes(previousDatabaseType, previousLogDatabaseType)
		common.SQLitePath = previousSQLitePath
		common.IsMasterNode = previousMasterNode
		common.RedisEnabled = previousRedisEnabled
	})

	require.NoError(t, database.Create(&model.User{
		Id:          91,
		Username:    "jimeng-fetch-user",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Quota:       100,
		Group:       "default",
		AuthVersion: 1,
	}).Error)
	require.NoError(t, database.Create(&model.Token{
		Id:             1,
		UserId:         91,
		Key:            "jimengfetch",
		Status:         common.TokenStatusEnabled,
		Name:           "jimeng fetch",
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)
	require.NoError(t, database.Create(&model.Channel{
		Id:     17,
		Type:   constant.ChannelTypeJimeng,
		Key:    "unused",
		Status: common.ChannelStatusEnabled,
		Name:   "jimeng fetch",
		Models: "jimeng_vgfm_t2v_l20",
		Group:  "default",
	}).Error)

	task := &model.Task{
		CreatedAt: 1710000000,
		UpdatedAt: 1710000060,
		TaskID:    "task_jimeng_public",
		Platform:  constant.TaskPlatform("jimeng"),
		UserId:    91,
		Group:     "default",
		ChannelId: 17,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: "data:video/mp4;base64,ZGF0YQ==",
		},
	}
	task.SetData(map[string]any{
		"code": 10000,
		"data": map[string]any{
			"status":    "done",
			"task_id":   "jimeng-private-1",
			"video_url": "https://cdn.example/video.mp4",
		},
		"message": "success",
	})
	require.NoError(t, database.Create(task).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	SetTaskRouter(engine)
	request := httptest.NewRequest(http.MethodGet, "/v1/videos/task_jimeng_public", nil)
	request.Header.Set("Authorization", "Bearer sk-jimengfetch")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Status      string `json:"status"`
		Progress    int    `json:"progress"`
		CreatedAt   int64  `json:"created_at"`
		CompletedAt int64  `json:"completed_at"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "task_jimeng_public", response.ID)
	assert.Equal(t, "video", response.Object)
	assert.Equal(t, "completed", response.Status)
	assert.Equal(t, 100, response.Progress)
	assert.Equal(t, int64(1710000000), response.CreatedAt)
	assert.Equal(t, int64(1710000060), response.CompletedAt)
	assert.NotContains(t, recorder.Body.String(), "cdn.example")
	assert.NotContains(t, recorder.Body.String(), "jimeng-private-1")

	for _, testCase := range []struct {
		name          string
		authorization string
		query         string
		wantStatus    int
	}{
		{name: "missing credential rejected", wantStatus: http.StatusUnauthorized},
		{name: "access rejected", query: "?access=not-a-video-credential", wantStatus: http.StatusUnauthorized},
		{name: "bearer accepted", authorization: "Bearer sk-jimengfetch", wantStatus: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"/v1/videos/task_jimeng_public/content"+testCase.query,
				nil,
			)
			if testCase.authorization != "" {
				request.Header.Set("Authorization", testCase.authorization)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			assert.Equal(t, testCase.wantStatus, recorder.Code, recorder.Body.String())
			if testCase.wantStatus == http.StatusOK {
				assert.Equal(t, "data", recorder.Body.String())
			}
		})
	}
	t.Run("shared concurrency cap covers all create surfaces and preserves reads", func(t *testing.T) {
		require.NoError(t, database.Model(&model.User{}).Where("id = ?", 91).Update("max_concurrent_requests", 1).Error)
		require.NoError(t, database.Create(&model.Token{Id: 2, UserId: 91, Key: "secondconcurrencykey", Status: common.TokenStatusEnabled, Name: "second key", ExpiredTime: -1, UnlimitedQuota: true}).Error)
		lease, err := service.AcquireRelayConcurrency(context.Background(), 91, 1, 1)
		require.NoError(t, err)
		defer func() { require.NoError(t, lease.Release(context.Background())) }()
		for _, endpoint := range []struct{ method, path string }{
			{http.MethodPost, "/v1/chat/completions"},
			{http.MethodPost, "/v1/responses"},
			{http.MethodGet, "/v1/responses"},
			{http.MethodGet, "/v1/realtime"},
			{http.MethodPost, "/v1/images/generations"},
			{http.MethodPost, "/v1/images/edits"},
			{http.MethodPost, "/v1/videos"},
			{http.MethodPost, "/v1/video/generations"},
			{http.MethodPost, "/v1/videos/task_jimeng_public/remix"},
			{http.MethodPost, "/v1/tasks/jimeng"},
			{http.MethodPost, "/v1beta/models/gemini-pro:generateContent"},
			{http.MethodPost, "/mj/submit/imagine"},
		} {
			t.Run(endpoint.method+endpoint.path, func(t *testing.T) {
				for _, authorized := range []bool{false, true} {
					request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
					request.Header.Set("Content-Type", "application/json")
					want := http.StatusUnauthorized
					if authorized {
						request.Header.Set("Authorization", "Bearer sk-secondconcurrencykey")
						want = http.StatusTooManyRequests
					}
					response := httptest.NewRecorder()
					engine.ServeHTTP(response, request)
					assert.Equal(t, want, response.Code, response.Body.String())
				}
			})
		}
		for _, path := range []string{"/v1/videos/task_jimeng_public", "/v1/videos/task_jimeng_public/content", "/v1/tasks/task_jimeng_public", "/v1/tasks/task_jimeng_public/artifacts"} {
			t.Run("read "+path, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set("Authorization", "Bearer sk-secondconcurrencykey")
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, request)
				assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
			})
		}
		snapshot, err := service.GetRelayConcurrency(context.Background(), 91, []int{1, 2})
		require.NoError(t, err)
		assert.Equal(t, 1, snapshot.Current)
		assert.Equal(t, 1, snapshot.Tokens[1])
		assert.Zero(t, snapshot.Tokens[2])
	})

}
