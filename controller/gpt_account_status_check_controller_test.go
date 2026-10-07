package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func statusCheckRequest(t *testing.T, identity service.AuthIdentity, role int, session bool, method, path, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", identity.UserID)
	c.Set("role", role)
	if session {
		c.Set("session_id", identity.SessionID)
		c.Set("auth_version", identity.UserAuthVersion)
		c.Set("session_version", identity.SessionVersion)
	}
	if id := strings.TrimPrefix(path, "/api/gpt-account-status-checks/"); id != path {
		id = strings.TrimSuffix(id, "/cancel")
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	handler(c)
	return recorder
}

func TestGPTAccountStatusCheckControllerRejectsInvalidDashboardAccess(t *testing.T) {
	identity := setupGPTAccountTest(t)
	require.NoError(t, authz.SetUserPermissions(identity.UserID, authz.PermissionsMap{authz.GPTAccountManage.Resource: {authz.GPTAccountManage.Action: false}}))
	t.Cleanup(func() { require.NoError(t, authz.ClearUserAuthorization(identity.UserID)) })
	body := `{"credentials":"alice@example.com----password----JBSWY3DPEHPK3PXP"}`
	for _, tc := range []struct {
		name    string
		role    int
		session bool
		status  int
	}{
		{name: "non-admin", role: common.RoleCommonUser, session: true, status: http.StatusForbidden},
		{name: "missing live session", role: common.RoleRootUser, session: false, status: http.StatusForbidden},
		{name: "missing permission", role: common.RoleAdminUser, session: true, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := statusCheckRequest(t, identity, tc.role, tc.session, http.MethodPost, "/api/gpt-account-status-checks", body, CreateGPTAccountStatusCheck)
			assert.Equal(t, tc.status, response.Code)
			assert.NotContains(t, response.Body.String(), "password")
		})
	}
}

func TestGPTAccountStatusCheckControllerRejectsMalformedAndOversizedBodies(t *testing.T) {
	t.Setenv("GPT_ACCOUNT_WORKER_URL", "")
	identity := setupGPTAccountTest(t)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"credentials":"bad"}`, http.StatusBadRequest},
		{`{"credentials":""}`, http.StatusBadRequest},
		{`{"credentials":"alice@example.com----password----JBSWY3DPEHPK3PXP","model_test":{"model":"../../x","prompt":"test"}}`, http.StatusBadRequest},
		{"{" + fmt.Sprintf("\"credentials\":%q", strings.Repeat("x", (256<<10)+1)) + "}", http.StatusRequestEntityTooLarge},
	} {
		response := statusCheckRequest(t, identity, common.RoleRootUser, true, http.MethodPost, "/api/gpt-account-status-checks", tc.body, CreateGPTAccountStatusCheck)
		assert.Equal(t, tc.status, response.Code)
		assert.NotContains(t, response.Body.String(), strings.Repeat("x", 64))
		// A regression must fail the assertion without leaving an unexpected job
		// running against the next test's database fixture.
		if response.Code == http.StatusOK {
			var payload struct {
				Data service.GPTAccountStatusCheckView `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.Eventually(t, func() bool {
				view, err := service.GetGPTAccountStatusCheck(identity, payload.Data.JobID)
				return err == nil && view.State == "completed"
			}, 3*time.Second, 10*time.Millisecond)
		}
	}
}

func TestGPTAccountStatusCheckControllerCreatePollCancelAndOwnerIsolation(t *testing.T) {
	t.Setenv("GPT_ACCOUNT_WORKER_URL", "")
	identity := setupGPTAccountTest(t)
	body := `{"credentials":"alice@example.com----password----JBSWY3DPEHPK3PXP","model_test":{"model":"gpt-6.1-sol","prompt":"Reply with 42","expected_answer":"42"}}`
	created := statusCheckRequest(t, identity, common.RoleRootUser, true, http.MethodPost, "/api/gpt-account-status-checks", body, CreateGPTAccountStatusCheck)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.Equal(t, "no-store", created.Header().Get("Cache-Control"))
	var payload struct {
		Data service.GPTAccountStatusCheckView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(created.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Data.JobID)
	assert.NotContains(t, created.Body.String(), "password")
	assert.Contains(t, created.Body.String(), `"model_test":{"model":"gpt-6.1-sol","prompt":"Reply with 42","expected_answer":"42"}`)

	poll := statusCheckRequest(t, identity, common.RoleRootUser, true, http.MethodGet, "/api/gpt-account-status-checks/"+payload.Data.JobID, "", GetGPTAccountStatusCheck)
	require.Equal(t, http.StatusOK, poll.Code, poll.Body.String())
	assert.Equal(t, "no-store", poll.Header().Get("Cache-Control"))

	other := identity
	other.UserID++
	owned := statusCheckRequest(t, other, common.RoleRootUser, true, http.MethodGet, "/api/gpt-account-status-checks/"+payload.Data.JobID, "", GetGPTAccountStatusCheck)
	assert.Equal(t, http.StatusNotFound, owned.Code)

	require.Eventually(t, func() bool {
		view, err := service.GetGPTAccountStatusCheck(identity, payload.Data.JobID)
		return err == nil && view.State == "completed"
	}, 3*time.Second, 10*time.Millisecond)
	cancel := statusCheckRequest(t, identity, common.RoleRootUser, true, http.MethodPost, "/api/gpt-account-status-checks/"+payload.Data.JobID+"/cancel", `{}`, CancelGPTAccountStatusCheck)
	require.Equal(t, http.StatusOK, cancel.Code, cancel.Body.String())
	assert.Equal(t, "no-store", cancel.Header().Get("Cache-Control"))
	assert.NotContains(t, cancel.Body.String(), "password")
}
