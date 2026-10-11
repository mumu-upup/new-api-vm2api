package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGPTAccountStatusCheckLines(t *testing.T) {
	creds, err := ParseGPTAccountStatusCheckLines(" alice@example.com----password----JBSWY3DPEHPK3PXP \n")
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, "alice@example.com", creds[0].Email)
	assert.Equal(t, "password", creds[0].Password)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", creds[0].TOTPSecret)

	for _, raw := range []string{
		"a@example.com----p----JBSWY3DPEHPK3PXP----extra",
		"a@example.com----p----not-base32!",
		"a@example.com----p----AAAAAAAAAAAAAAAAAB",
		"a@example.com----p----JBSWY3DPEHPK3PXP\na@example.com----p2----JBSWY3DPEHPK3PXP",
		"----p----JBSWY3DPEHPK3PXP",
	} {
		_, err := ParseGPTAccountStatusCheckLines(raw)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "password")
		assert.NotContains(t, err.Error(), "JBSWY3D")
	}
	_, err = ParseGPTAccountStatusCheckLines(strings.Repeat("a@example.com----p----JBSWY3DPEHPK3PXP\n", 101))
	require.Error(t, err)
}

func TestGPTAccountErrorCodePreservesSessionCleanupFailures(t *testing.T) {
	for _, code := range []string{"session_cleanup_unverified", "session_cleanup_failed", "session_cleanup_unsupported"} {
		assert.Equal(t, code, GPTAccountErrorCode(errors.New(code)))
	}
}

func statusCheckFixture(t *testing.T, handler http.HandlerFunc) AuthIdentity {
	t.Helper()
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "checker-test")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("GPT_ACCOUNT_WORKER_URL", server.URL)
	t.Setenv("GPT_ACCOUNT_WORKER_TOKEN_FILE", tokenFile)
	t.Cleanup(func() {
		gptAccountStatusChecks.Lock()
		jobs := make([]*gptAccountStatusCheckJob, 0, len(gptAccountStatusChecks.jobs))
		for _, job := range gptAccountStatusChecks.jobs {
			jobs = append(jobs, job)
		}
		gptAccountStatusChecks.Unlock()
		for _, job := range jobs {
			expireGPTAccountStatusCheck(job.jobID)
			select {
			case <-job.done:
			case <-time.After(3 * time.Second):
				t.Error("job did not stop")
			}
		}
	})
	return identity
}

func startStatusCheck(t *testing.T, identity AuthIdentity, rows int) *gptAccountStatusCheckJob {
	t.Helper()
	credentials := []GPTAccountStatusCheckCredential{{Email: "alice@example.com", Password: " private----pass ", TOTPSecret: "JBSWY3DPEHPK3PXP"}}
	if rows == 2 {
		credentials = append(credentials, GPTAccountStatusCheckCredential{Email: "bob@example.com", Password: "second", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	}
	view, err := CreateGPTAccountStatusCheck(identity.UserID, identity, credentials)
	require.NoError(t, err)
	gptAccountStatusChecks.Lock()
	job := gptAccountStatusChecks.jobs[view.JobID]
	gptAccountStatusChecks.Unlock()
	require.NotNil(t, job)
	return job
}

func waitStatusCheck(t *testing.T, job *gptAccountStatusCheckJob) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
}

func TestGPTAccountStatusCheckSuccessAndIsolation(t *testing.T) {
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() { common.LogWriterMu.Lock(); gin.DefaultWriter = previousWriter; common.LogWriterMu.Unlock() })
	var calls atomic.Int32
	identity := statusCheckFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/check", r.URL.Path)
		assert.Equal(t, "Bearer "+strings.Repeat("t", 32), r.Header.Get("Authorization"))
		var payload map[string]string
		if !assert.NoError(t, common.DecodeJson(r.Body, &payload)) {
			return
		}
		assert.Equal(t, " private----pass ", payload["password"])
		assert.Equal(t, "local", payload["totp_provider"])
		_, _ = w.Write([]byte(`{"status":"success","code":"must-not-return","password":"private"}`))
	})
	job := startStatusCheck(t, identity, 1)
	waitStatusCheck(t, job)
	view, err := GetGPTAccountStatusCheck(identity, job.jobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", view.State)
	assert.Equal(t, 1, view.Success)
	assert.Equal(t, "a***@example.com", view.Results[0].Email)
	assert.EqualValues(t, 1, calls.Load())
	encoded, err := common.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private")
	assert.NotContains(t, string(encoded), "must-not-return")
	assert.NotContains(t, string(encoded), "JBSWY3D")
	for _, changed := range []AuthIdentity{
		{UserID: identity.UserID + 1, SessionID: identity.SessionID},
		{UserID: identity.UserID, SessionID: "different"},
		{UserID: identity.UserID, SessionID: identity.SessionID, UserAuthVersion: identity.UserAuthVersion + 1, SessionVersion: identity.SessionVersion},
	} {
		_, err = GetGPTAccountStatusCheck(changed, job.jobID)
		require.Error(t, err)
		require.Error(t, CancelGPTAccountStatusCheck(changed, job.jobID))
	}
	assert.Empty(t, job.credentials[0].Password)
	assert.Empty(t, job.credentials[0].TOTPSecret)
	common.LogWriterMu.RLock()
	recorded := logs.String()
	common.LogWriterMu.RUnlock()
	assert.Contains(t, recorded, "gpt_account_status_check")
	assert.Contains(t, recorded, job.jobID)
	assert.Contains(t, recorded, "status=success")
	for _, secret := range []string{"alice@example.com", "private", "JBSWY3D", "must-not-return"} {
		assert.NotContains(t, recorded, secret)
	}
}

func TestGPTAccountStatusCheckCancellationAndExpiryStopQueuedCredentials(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "expiry"}[expire], func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			identity := statusCheckFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) != 1 {
					t.Error("queued credentials reached worker")
					return
				}
				var payload map[string]string
				if !assert.NoError(t, common.DecodeJson(r.Body, &payload)) {
					return
				}
				close(started)
				<-r.Context().Done()
				close(stopped)
			})
			job := startStatusCheck(t, identity, 2)
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("worker not reached")
			}
			_, err := CreateGPTAccountStatusCheck(identity.UserID, identity, []GPTAccountStatusCheckCredential{{Email: "other@example.com", Password: "p", TOTPSecret: "JBSWY3DPEHPK3PXP"}})
			require.EqualError(t, err, "job_active")
			if expire {
				expireGPTAccountStatusCheck(job.jobID)
			} else {
				require.NoError(t, CancelGPTAccountStatusCheck(identity, job.jobID))
			}
			waitStatusCheck(t, job)
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("worker not cancelled")
			}
			assert.EqualValues(t, 1, calls.Load())
			view, err := GetGPTAccountStatusCheck(identity, job.jobID)
			if expire {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "cancelled", view.State)
				assert.Equal(t, 2, view.Cancelled)
				assert.Equal(t, "b***@example.com", view.Results[1].Email)
			}
			for _, credential := range job.credentials {
				assert.Empty(t, credential)
			}
		})
	}
}

func TestGPTAccountStatusCheckWaitsForWorkerAndHonorsRevocation(t *testing.T) {
	var calls atomic.Int32
	identity := statusCheckFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	gptAccountStatusChecks.sem <- struct{}{}
	job := startStatusCheck(t, identity, 1)
	// Hold the queue at the actual worker boundary while revoking the initiating session.
	_, err := model.RevokeUserSession(identity.UserID, identity.SessionID, "test")
	require.NoError(t, err)
	<-gptAccountStatusChecks.sem
	waitStatusCheck(t, job)
	view, err := GetGPTAccountStatusCheck(identity, job.jobID)
	require.NoError(t, err)
	assert.Equal(t, "authorization_invalid", view.Results[0].ErrorCode)
	assert.EqualValues(t, 0, calls.Load())
}

func TestGPTAccountStatusCheckWorkerErrorsAreSafe(t *testing.T) {
	for _, tc := range []struct{ response, status, code string }{
		{`{"status":"needs_attention","error_code":"totp_not_challenged"}`, "needs_attention", "totp_not_challenged"},
		{`{"status":"needs_attention","error_code":"password_step_timeout"}`, "needs_attention", "password_step_timeout"},
		{`{"status":"failed","error_code":"secret-password"}`, "failed", "automation_unavailable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			statusCheckFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.response)) })
			result := checkGPTAccountStatusCredential(context.Background(), GPTAccountStatusCheckCredential{Email: "a@example.com", Password: "p", TOTPSecret: "JBSWY3DPEHPK3PXP"}, nil, nil)
			assert.Equal(t, tc.status, result.Status)
			assert.Equal(t, tc.code, result.ErrorCode)
		})
	}
}

func TestValidateGPTAccountModelTestOptions(t *testing.T) {
	require.NoError(t, ValidateGPTAccountModelTestOptions(nil))
	for _, tc := range []struct {
		name    string
		options GPTAccountModelTestOptions
		valid   bool
	}{
		{"valid", GPTAccountModelTestOptions{Model: "gpt-5.4:latest", Prompt: "What is 1 + 1?", ExpectedAnswer: "2"}, true},
		{"limits", GPTAccountModelTestOptions{Model: strings.Repeat("m", 100), Prompt: strings.Repeat("p", 4000), ExpectedAnswer: strings.Repeat("a", 1000)}, true},
		{"blank_prompt", GPTAccountModelTestOptions{Model: "gpt-5", Prompt: " \r\n\t"}, false},
		{"prompt_bytes", GPTAccountModelTestOptions{Model: "gpt-5", Prompt: strings.Repeat("中", 1334)}, false},
		{"answer_bytes", GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "hello", ExpectedAnswer: strings.Repeat("中", 334)}, false},
		{"model_length", GPTAccountModelTestOptions{Model: strings.Repeat("m", 101), Prompt: "hello"}, false},
		{"model_path", GPTAccountModelTestOptions{Model: "gpt/5", Prompt: "hello"}, false},
		{"model_prefix", GPTAccountModelTestOptions{Model: "-gpt5", Prompt: "hello"}, false},
		{"model_unicode", GPTAccountModelTestOptions{Model: "gpt５", Prompt: "hello"}, false},
		{"invalid_utf8_prompt", GPTAccountModelTestOptions{Model: "gpt-5", Prompt: string([]byte{0xff})}, false},
		{"invalid_utf8_answer", GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "hello", ExpectedAnswer: string([]byte{0xff})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGPTAccountModelTestOptions(&tc.options)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, "invalid_model_test")
			}
		})
	}
}

type gptModelProbeTransport func(*http.Request) (*http.Response, error)

func (f gptModelProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Real signed ID tokens and issuer keys exercise the actual OIDC verifier. Only
// outbound HTTP is replaced; all production URLs and request bodies are checked.
type gptModelProbeFixture struct {
	key         *rsa.PrivateKey
	claims      jwt.MapClaims
	tokenFields map[string]any
	respond     func(*http.Request) (*http.Response, error)
	calls       []string
}

func newGPTModelProbeFixture(t *testing.T) *gptModelProbeFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("private-access-token"))
	return &gptModelProbeFixture{
		key: key,
		claims: jwt.MapClaims{
			"iss": "https://auth.openai.com", "sub": "subject-alice", "aud": codexOAuthClientID,
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": "test-nonce",
			"email": "Alice@Example.com", "email_verified": true,
			"at_hash":                     base64.RawURLEncoding.EncodeToString(hash[:16]),
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-alice"},
		},
		tokenFields: map[string]any{"access_token": "private-access-token", "token_type": "Bearer", "expires_in": 3600},
	}
}

func gptModelProbeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func (f *gptModelProbeFixture) client(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: gptModelProbeTransport(func(r *http.Request) (*http.Response, error) {
		f.calls = append(f.calls, r.URL.String())
		deadline, exists := r.Context().Deadline()
		assert.True(t, exists)
		assert.LessOrEqual(t, time.Until(deadline), 90*time.Second)
		switch r.URL.String() {
		case codexOAuthTokenURL:
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			assert.Equal(t, "private-code", r.Form.Get("code"))
			assert.Equal(t, strings.Repeat("v", 43), r.Form.Get("code_verifier"))
			assert.Equal(t, codexOAuthClientID, r.Form.Get("client_id"))
			assert.Equal(t, gptOAuthRedirect, r.Form.Get("redirect_uri"))
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims)
			token.Header["kid"] = "probe-test"
			signed, err := token.SignedString(f.key)
			require.NoError(t, err)
			fields := map[string]any{"id_token": signed}
			for name, value := range f.tokenFields {
				fields[name] = value
			}
			body, err := common.Marshal(fields)
			require.NoError(t, err)
			return gptModelProbeResponse(http.StatusOK, string(body)), nil
		case "https://auth.openai.com/.well-known/jwks.json":
			body, err := common.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "probe-test", Algorithm: "RS256", Use: "sig"}}})
			require.NoError(t, err)
			return gptModelProbeResponse(http.StatusOK, string(body)), nil
		case "https://chatgpt.com/backend-api/codex/responses":
			assert.Equal(t, "Bearer private-access-token", r.Header.Get("Authorization"))
			assert.Equal(t, "account-alice", r.Header.Get("chatgpt-account-id"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
			var request map[string]any
			require.NoError(t, common.DecodeJson(r.Body, &request))
			assert.Equal(t, "gpt-5", request["model"])
			assert.Equal(t, false, request["store"])
			assert.Equal(t, true, request["stream"])
			assert.Contains(t, request, "instructions")
			assert.NotContains(t, request, "max_output_tokens")
			assert.Empty(t, request["tools"])
			assert.Equal(t, []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "What is 1 + 1?"}}}}, request["input"])
			if f.respond != nil {
				return f.respond(r)
			}
			return gptModelProbeResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-2026\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"2\"}]}]}}\n\n"), nil
		default:
			t.Errorf("unexpected outbound URL: %s", r.URL.String())
			return nil, errors.New("unexpected destination")
		}
	})}
}

func (f *gptModelProbeFixture) run(t *testing.T, expected string) GPTAccountModelTestResult {
	t.Helper()
	return runGPTAccountModelTest(context.Background(), f.client(t), GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?", ExpectedAnswer: expected}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", func() bool { return true })
}

func TestGPTAccountModelTestSuccessAndGrading(t *testing.T) {
	for _, tc := range []struct{ name, expected, evaluation string }{
		{"matched", " \r\n2 \n", "matched"},
		{"mismatch", "two", "mismatch"},
		{"unscored", " \t", "unscored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			// A refresh token is optional and even an unrelated JSON value is ignored.
			f.tokenFields["refresh_token"] = map[string]any{"discard": true}
			result := f.run(t, tc.expected)
			assert.Equal(t, "success", result.Status)
			assert.Empty(t, result.ErrorCode)
			assert.Equal(t, "2", result.Answer)
			assert.Equal(t, "gpt-5", result.Model)
			assert.Equal(t, "gpt-5-2026", result.ActualModel)
			assert.Equal(t, tc.evaluation, result.Evaluation)
			assert.GreaterOrEqual(t, result.ElapsedMS, int64(0))
			encoded, err := common.Marshal(result)
			require.NoError(t, err)
			for _, secret := range []string{"private-access-token", "private-code", "test-nonce", "alice@example.com", "What is 1 + 1?"} {
				assert.NotContains(t, string(encoded), secret)
			}
			assert.Len(t, f.calls, 3)
		})
	}
}

func TestGPTAccountModelTestNewlineNormalizationIsCaseSensitive(t *testing.T) {
	f := newGPTModelProbeFixture(t)
	// at_hash is optional in the authorization-code flow.
	delete(f.claims, "at_hash")
	f.respond = func(*http.Request) (*http.Response, error) {
		return gptModelProbeResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Yes\\r\\nTwo\"}]}]}}\n\n"), nil
	}
	assert.Equal(t, "matched", f.run(t, " Yes\nTwo ").Evaluation)
	assert.Equal(t, "mismatch", f.run(t, "yes\nTwo").Evaluation)
}

func TestGPTAccountModelTestRejectsUnverifiedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, errorCode string
		change          func(*testing.T, *gptModelProbeFixture)
	}{
		{"nonce", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["nonce"] = "another-nonce" }},
		{"issuer", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["iss"] = "https://attacker.invalid" }},
		{"audience", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["aud"] = "other-client" }},
		{"authorized_party", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) {
			f.claims["aud"] = []string{codexOAuthClientID, "other-client"}
			f.claims["azp"] = "other-client"
		}},
		{"expired", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{"missing_expiry", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { delete(f.claims, "exp") }},
		{"not_yet_valid", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["nbf"] = time.Now().Add(time.Hour).Unix() }},
		{"missing_nonce", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { delete(f.claims, "nonce") }},
		{"missing_subject", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { delete(f.claims, "sub") }},
		{"email", "identity_mismatch", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["email"] = "mallory@example.com" }},
		{"unverified_email", "identity_mismatch", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["email_verified"] = false }},
		{"account", "identity_mismatch", func(_ *testing.T, f *gptModelProbeFixture) {
			f.claims["https://api.openai.com/auth"] = map[string]any{}
		}},
		{"account_header_injection", "identity_mismatch", func(_ *testing.T, f *gptModelProbeFixture) {
			f.claims["https://api.openai.com/auth"] = map[string]any{"chatgpt_account_id": "account\r\ninjected: true"}
		}},
		{"account_control_character", "identity_mismatch", func(_ *testing.T, f *gptModelProbeFixture) {
			f.claims["https://api.openai.com/auth"] = map[string]any{"chatgpt_account_id": "account\x00"}
		}},
		{"access_hash", "identity_verification_failed", func(_ *testing.T, f *gptModelProbeFixture) { f.claims["at_hash"] = "wrong-hash" }},
		{"signature", "identity_verification_failed", func(t *testing.T, f *gptModelProbeFixture) {
			other, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims)
			signed, err := token.SignedString(other)
			require.NoError(t, err)
			f.tokenFields["id_token"] = signed
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			tc.change(t, f)
			result := f.run(t, "2")
			assert.Equal(t, "failed", result.Status)
			assert.Equal(t, tc.errorCode, result.ErrorCode)
			assert.Equal(t, "unscored", result.Evaluation)
			assert.Empty(t, result.Answer)
			assert.NotContains(t, f.calls, "https://chatgpt.com/backend-api/codex/responses")
		})
	}
}

func TestGPTAccountModelTestStreamContracts(t *testing.T) {
	delta := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"2\"}\n\n"
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"2\"}]}]}}\n\n"
	for _, tc := range []struct{ name, body, code, answer string }{
		{"delta_and_final_do_not_duplicate", delta + completed, "", "2"},
		{"delta_with_terminal", delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "", "2"},
		{"eof_after_delta", delta, "model_response_incomplete", ""},
		{"done_is_not_completion", delta + "data: [DONE]\n\n", "model_response_incomplete", ""},
		{"empty_completed", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "model_no_answer", ""},
		{"nonassistant_output", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"output_text\",\"text\":\"2\"}]}]}}\n\n", "model_no_answer", ""},
		{"invalid_json", "data: {broken}\n\n", "model_response_invalid", ""},
		{"incomplete", delta + "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", "model_response_incomplete", ""},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"model_not_found\",\"message\":\"private-access-token\"}}}\n\n", "model_not_available", ""},
		{"error", "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"private-access-token\"}\n\n", "model_rate_limited", ""},
		{"contradictory_terminal", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", "model_response_invalid", ""},
		{"completed_with_error", strings.Replace(completed, `"status":"completed"`, `"status":"completed","error":{"message":"failed"}`, 1), "model_response_invalid", ""},
		{"answer_limit", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", (32<<10)+1) + "\"}\n\n" + completed, "model_output_too_large", ""},
		{"final_answer_limit", strings.Replace(completed, `"text":"2"`, `"text":"`+strings.Repeat("x", (32<<10)+1)+`"`, 1), "model_output_too_large", ""},
		{"answer_at_limit", strings.Replace(completed, `"text":"2"`, `"text":"`+strings.Repeat("x", 32<<10)+`"`, 1), "", strings.Repeat("x", 32<<10)},
		{"stream_limit", ":" + strings.Repeat("x", (1<<20)+1) + "\n\n" + completed, "model_output_too_large", ""},
		{"event_limit", strings.Repeat(": ping\n\n", (1<<20)/8+1) + completed, "model_output_too_large", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			f.respond = func(*http.Request) (*http.Response, error) { return gptModelProbeResponse(http.StatusOK, tc.body), nil }
			result := f.run(t, "2")
			assert.Equal(t, tc.code, result.ErrorCode)
			assert.Equal(t, tc.answer, result.Answer)
			if tc.code == "" {
				assert.Equal(t, "success", result.Status)
			} else {
				assert.Equal(t, "failed", result.Status)
				assert.Equal(t, "unscored", result.Evaluation)
			}
		})
	}
}

func TestGPTAccountModelTestHTTPFailuresAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{401, `{"error":{"message":"private-access-token"}}`, "model_authentication_failed"},
		{403, `{"error":{"code":"permission_denied"}}`, "model_access_denied"},
		{404, `{"error":{"code":"model_not_found"}}`, "model_not_available"},
		{429, `{"error":{"code":"insufficient_quota"}}`, "model_quota_exhausted"},
		{429, `{"error":{"type":"rate_limit_exceeded"}}`, "model_rate_limited"},
		{500, `{"error":{"message":"private-code"}}`, "model_upstream_error"},
		{302, "", "model_upstream_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			f.respond = func(*http.Request) (*http.Response, error) {
				response := gptModelProbeResponse(tc.status, tc.body)
				response.Header.Set("Location", "https://chatgpt.com/leak-token")
				return response, nil
			}
			result := f.run(t, "2")
			assert.Equal(t, tc.code, result.ErrorCode)
			assert.Equal(t, "failed", result.Status)
			assert.Len(t, f.calls, 3, "never retry a model request or follow a redirect")
		})
	}
}

func TestGPTAccountModelTestCancellationAndAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		authorized func() bool
		cancelled  bool
		code       string
	}{
		{"missing_authorization", nil, false, "authorization_invalid"},
		{"revoked", func() bool { return false }, false, "authorization_invalid"},
		{"cancelled", func() bool { return true }, true, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			result := runGPTAccountModelTest(ctx, f.client(t), GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?"}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", tc.authorized)
			assert.Equal(t, tc.code, result.ErrorCode)
			assert.Empty(t, f.calls)
		})
	}
	t.Run("revoked_before_inference", func(t *testing.T) {
		f := newGPTModelProbeFixture(t)
		calls := 0
		result := runGPTAccountModelTest(context.Background(), f.client(t), GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?"}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", func() bool { calls++; return calls == 1 })
		assert.Equal(t, "authorization_invalid", result.ErrorCode)
		assert.NotContains(t, f.calls, "https://chatgpt.com/backend-api/codex/responses")
	})
	t.Run("cancel_during_inference", func(t *testing.T) {
		f := newGPTModelProbeFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.respond = func(r *http.Request) (*http.Response, error) { cancel(); return nil, r.Context().Err() }
		result := runGPTAccountModelTest(ctx, f.client(t), GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?"}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", func() bool { return true })
		assert.Equal(t, "cancelled", result.Status)
		assert.Equal(t, "cancelled", result.ErrorCode)
	})
}

func TestGPTAccountModelTestOAuthTransportBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, stage, code string }{
		{"token_redirect", codexOAuthTokenURL, "authorization_failed"},
		{"keys_redirect", "https://auth.openai.com/.well-known/jwks.json", "identity_verification_failed"},
		{"token_timeout", codexOAuthTokenURL, "model_timeout"},
		{"keys_timeout", "https://auth.openai.com/.well-known/jwks.json", "model_timeout"},
		{"token_body_timeout", codexOAuthTokenURL, "model_timeout"},
		{"keys_body_timeout", "https://auth.openai.com/.well-known/jwks.json", "model_timeout"},
		{"token_network_error", codexOAuthTokenURL, "model_network_error"},
		{"keys_cancelled", "https://auth.openai.com/.well-known/jwks.json", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGPTModelProbeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := f.client(t)
			base := client.Transport
			client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy must not run"); return nil }
			client.Transport = gptModelProbeTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != tc.stage {
					return base.RoundTrip(r)
				}
				if strings.HasSuffix(tc.name, "redirect") {
					response := gptModelProbeResponse(http.StatusTemporaryRedirect, "")
					response.Header.Set("Location", "https://auth.openai.com/other-token-path")
					return response, nil
				}
				if strings.HasSuffix(tc.name, "body_timeout") {
					response := gptModelProbeResponse(http.StatusOK, "")
					response.Body = io.NopCloser(iotest.ErrReader(context.DeadlineExceeded))
					return response, nil
				}
				if strings.HasSuffix(tc.name, "timeout") {
					return nil, context.DeadlineExceeded
				}
				if tc.code == "cancelled" {
					cancel()
					return nil, r.Context().Err()
				}
				return nil, errors.New("private-code")
			})
			result := runGPTAccountModelTest(ctx, client, GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?"}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", func() bool { return true })
			assert.Equal(t, tc.code, result.ErrorCode)
			assert.EqualValues(t, 0, result.ElapsedMS, "model latency excludes login and OAuth")
			assert.NotContains(t, f.calls, "https://chatgpt.com/backend-api/codex/responses")
		})
	}
}

func TestGPTAccountModelTestCancellationClosesStreamingConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	f := newGPTModelProbeFixture(t)
	f.respond = func(r *http.Request) (*http.Response, error) {
		forwarded := r.Clone(r.Context())
		forwarded.URL = serverURL
		forwarded.Host = serverURL.Host
		forwarded.Body, forwarded.GetBody, forwarded.ContentLength = nil, nil, 0
		return server.Client().Transport.RoundTrip(forwarded)
	}
	go func() { <-started; cancel() }()
	result := runGPTAccountModelTest(ctx, f.client(t), GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "What is 1 + 1?"}, "alice@example.com", "private-code", strings.Repeat("v", 43), "test-nonce", func() bool { return true })
	assert.Equal(t, "cancelled", result.Status)
	assert.Empty(t, result.Answer)
	assert.Equal(t, "unscored", result.Evaluation)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream stream remained open after cancellation")
	}
}

func TestGPTAccountStatusCheckProbeRejectsInvalidCallback(t *testing.T) {
	for _, missingCode := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong_state", true: "missing_code"}[missingCode], func(t *testing.T) {
			var states, nonces []string
			statusCheckFixture(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/probe", r.URL.Path)
				var payload map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &payload))
				assert.Equal(t, "local", payload["totp_provider"])
				authorization, err := url.Parse(payload["authorization_url"])
				require.NoError(t, err)
				assert.Equal(t, "https", authorization.Scheme)
				assert.Equal(t, "auth.openai.com", authorization.Host)
				query := authorization.Query()
				assert.Equal(t, "S256", query.Get("code_challenge_method"))
				assert.Len(t, query.Get("code_challenge"), 43)
				assert.Equal(t, payload["state"], query.Get("state"))
				assert.Len(t, query.Get("state"), 43)
				assert.Len(t, query.Get("nonce"), 43)
				assert.NotContains(t, strings.Fields(query.Get("scope")), "offline_access")
				states = append(states, query.Get("state"))
				nonces = append(nonces, query.Get("nonce"))
				response := map[string]string{"status": "success", "code": "private-code", "state": "wrong-state"}
				if missingCode {
					response["state"] = payload["state"]
					delete(response, "code")
				}
				body, err := common.Marshal(response)
				require.NoError(t, err)
				_, _ = w.Write(body)
			})
			for range 2 {
				result := checkGPTAccountStatusCredential(context.Background(), GPTAccountStatusCheckCredential{Email: "alice@example.com", Password: "p", TOTPSecret: "JBSWY3DPEHPK3PXP"}, &GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "hello"}, func() bool { return true })
				assert.Equal(t, "invalid_callback", result.ErrorCode)
				assert.Equal(t, "skipped", result.ModelTest.Status)
			}
			require.Len(t, states, 2)
			assert.NotEqual(t, states[0], states[1])
			assert.NotEqual(t, nonces[0], nonces[1])
		})
	}
}

func TestGPTAccountStatusCheckViewOwnsModelTestData(t *testing.T) {
	view := GPTAccountStatusCheckView{
		ModelTest: &GPTAccountModelTestOptions{Model: "gpt-5", Prompt: "hello", ExpectedAnswer: "hi"},
		Results:   []GPTAccountStatusCheckResult{{Status: "success", ModelTest: &GPTAccountModelTestResult{Status: "success", Answer: "hi", Evaluation: "matched"}}},
	}
	cloned := cloneGPTAccountStatusCheckView(view)
	cloned.ModelTest.Prompt = "changed"
	cloned.Results[0].Status = "failed"
	cloned.Results[0].ModelTest.Answer = "changed"
	assert.Equal(t, "hello", view.ModelTest.Prompt)
	assert.Equal(t, "success", view.Results[0].Status)
	assert.Equal(t, "hi", view.Results[0].ModelTest.Answer)
}

func TestParseGPTAccountStatusCheckLinesPreservesPassword(t *testing.T) {
	credentials, err := ParseGPTAccountStatusCheckLines(" Alice@Example.COM ---- p----word ----JBSWY3DPEHPK3PXP")
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", credentials[0].Email)
	assert.Equal(t, " p----word ", credentials[0].Password)
	for _, raw := range []string{"a@example.com----p\rword----JBSWY3DPEHPK3PXP", "a@example.com----" + strings.Repeat("中", 400) + "----JBSWY3DPEHPK3PXP"} {
		_, err := ParseGPTAccountStatusCheckLines(raw)
		require.Error(t, err)
	}
}
