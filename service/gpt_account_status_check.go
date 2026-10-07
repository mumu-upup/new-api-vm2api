package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

const (
	gptAccountStatusCheckMaxCredentials = 100
	gptAccountStatusCheckMaxJobs        = 32
	gptAccountStatusCheckMaxRuntime     = 30 * time.Minute
	gptAccountStatusCheckConcurrency    = 1
)

var gptAccountStatusCheckTTL = 15 * time.Minute

var gptAccountStatusCheckEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
var gptAccountStatusCheckCodePattern = regexp.MustCompile(`^[\x21-\x7e]+$`)
var gptAccountStatusCheckTOTPPattern = regexp.MustCompile(`^[A-Z2-7]+$`)

// GPTAccountStatusCheckCredential is one transient set of credentials. It is
// never persisted and is never included in a status view.
type GPTAccountStatusCheckCredential struct {
	Email      string
	Password   string
	TOTPSecret string
}

type GPTAccountStatusCheckResult struct {
	Email       string                     `json:"email"`
	Status      string                     `json:"status"`
	ErrorCode   string                     `json:"error_code,omitempty"`
	LoginStatus string                     `json:"login_status,omitempty"`
	DurationMS  int64                      `json:"duration_ms"`
	ModelTest   *GPTAccountModelTestResult `json:"model_test,omitempty"`
}

// GPTAccountStatusCheckView contains only aggregate progress and masked result
// categories. Passwords and TOTP secrets are intentionally absent.
type GPTAccountStatusCheckView struct {
	JobID          string                        `json:"job_id"`
	State          string                        `json:"state"`
	Total          int                           `json:"total"`
	Completed      int                           `json:"completed"`
	Success        int                           `json:"success"`
	Failed         int                           `json:"failed"`
	NeedsAttention int                           `json:"needs_attention"`
	Cancelled      int                           `json:"cancelled"`
	Results        []GPTAccountStatusCheckResult `json:"results"`
	ModelTest      *GPTAccountModelTestOptions   `json:"model_test,omitempty"`
}

type gptAccountStatusCheckJob struct {
	ownerID     int
	identity    AuthIdentity
	jobID       string
	ctx         context.Context
	cancel      context.CancelFunc
	credentials []GPTAccountStatusCheckCredential
	view        GPTAccountStatusCheckView
	maxRuntime  *time.Timer
	done        chan struct{}
}

var gptAccountStatusChecks = struct {
	sync.Mutex
	jobs map[string]*gptAccountStatusCheckJob
	sem  chan struct{}
}{jobs: make(map[string]*gptAccountStatusCheckJob), sem: make(chan struct{}, gptAccountStatusCheckConcurrency)}

// ParseGPTAccountStatusCheckLines validates operator-supplied transient rows.
func ParseGPTAccountStatusCheckLines(raw string) ([]GPTAccountStatusCheckCredential, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || len(lines) > gptAccountStatusCheckMaxCredentials {
		return nil, errors.New("invalid_credentials")
	}
	credentials := make([]GPTAccountStatusCheckCredential, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if strings.ContainsAny(line, "\r\n") {
			return nil, errors.New("invalid_credentials")
		}
		first, last := strings.Index(line, "----"), strings.LastIndex(line, "----")
		if first <= 0 || last <= first+4 {
			return nil, errors.New("invalid_credentials")
		}
		email := strings.ToLower(strings.TrimSpace(line[:first]))
		password := line[first+4 : last]
		secret := strings.TrimSpace(line[last+4:])
		if len(email) == 0 || len(email) > 254 || !gptAccountStatusCheckEmailPattern.MatchString(email) || len(password) == 0 || len(password) > 1024 {
			return nil, errors.New("invalid_credentials")
		}
		normalized, ok := normalizeGPTAccountStatusCheckTOTP(secret)
		if !ok {
			return nil, errors.New("invalid_credentials")
		}
		if _, exists := seen[email]; exists {
			return nil, errors.New("invalid_credentials")
		}
		seen[email] = struct{}{}
		credentials = append(credentials, GPTAccountStatusCheckCredential{Email: email, Password: password, TOTPSecret: normalized})
	}
	return credentials, nil
}

func normalizeGPTAccountStatusCheckTOTP(secret string) (string, bool) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	secret = strings.TrimRight(secret, "=")
	if len(secret) < 16 || len(secret) > 128 || !gptAccountStatusCheckTOTPPattern.MatchString(secret) {
		return "", false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	defer clear(decoded)
	if err != nil || len(decoded) == 0 || base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded) != secret {
		return "", false
	}
	return secret, true
}

// CreateGPTAccountStatusCheck starts transient, bounded status checks.
func CreateGPTAccountStatusCheck(ownerID int, identity AuthIdentity, credentials []GPTAccountStatusCheckCredential, modelTests ...*GPTAccountModelTestOptions) (*GPTAccountStatusCheckView, error) {
	defer clear(credentials)
	var modelTest *GPTAccountModelTestOptions
	if len(modelTests) > 1 {
		return nil, errors.New("invalid_model_test")
	}
	if len(modelTests) == 1 && modelTests[0] != nil {
		cloned := *modelTests[0]
		modelTest = &cloned
	}
	if err := ValidateGPTAccountModelTestOptions(modelTest); err != nil {
		return nil, err
	}
	if ownerID <= 0 || identity.UserID != ownerID || identity.SessionID == "" || identity.SessionVersion <= 0 || identity.UserAuthVersion <= 0 || len(credentials) == 0 || len(credentials) > gptAccountStatusCheckMaxCredentials {
		return nil, errors.New("invalid_request")
	}
	seen := make(map[string]struct{}, len(credentials))
	for i := range credentials {
		c := &credentials[i]
		c.Email = strings.ToLower(strings.TrimSpace(c.Email))
		if len(c.Email) == 0 || len(c.Email) > 254 || !gptAccountStatusCheckEmailPattern.MatchString(c.Email) || strings.ContainsAny(c.Email+c.Password, "\r\n") || len(c.Password) == 0 || len(c.Password) > 1024 {
			return nil, errors.New("invalid_credentials")
		}
		if _, exists := seen[c.Email]; exists {
			return nil, errors.New("invalid_credentials")
		}
		seen[c.Email] = struct{}{}
		secret, ok := normalizeGPTAccountStatusCheckTOTP(c.TOTPSecret)
		if !ok {
			return nil, errors.New("invalid_credentials")
		}
		// Detach fields from the batch request so unfinished rows cannot retain
		// the entire input buffer after an earlier row has been discarded.
		c.Email = strings.Clone(c.Email)
		c.Password = strings.Clone(c.Password)
		c.TOTPSecret = strings.Clone(secret)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobID := uuid.NewString()
	view := GPTAccountStatusCheckView{JobID: jobID, State: "pending", Total: len(credentials), Results: make([]GPTAccountStatusCheckResult, len(credentials))}
	view.ModelTest = modelTest
	for i := range credentials {
		view.Results[i] = GPTAccountStatusCheckResult{Email: maskGPTAccountStatusCheckEmail(credentials[i].Email), Status: "pending"}
	}
	job := &gptAccountStatusCheckJob{done: make(chan struct{}), ownerID: ownerID, identity: identity, jobID: jobID, ctx: ctx, cancel: cancel, credentials: append([]GPTAccountStatusCheckCredential(nil), credentials...), view: view}
	cloned := cloneGPTAccountStatusCheckView(view)
	gptAccountStatusChecks.Lock()
	if len(gptAccountStatusChecks.jobs) >= gptAccountStatusCheckMaxJobs {
		gptAccountStatusChecks.Unlock()
		cancel()
		return nil, errors.New("job_limit")
	}
	for _, existing := range gptAccountStatusChecks.jobs {
		if existing.ownerID == ownerID && (existing.view.State == "pending" || existing.view.State == "running") {
			gptAccountStatusChecks.Unlock()
			cancel()
			return nil, errors.New("job_active")
		}
	}
	job.maxRuntime = time.AfterFunc(gptAccountStatusCheckMaxRuntime, func() { expireGPTAccountStatusCheck(jobID) })
	gptAccountStatusChecks.jobs[jobID] = job
	gptAccountStatusChecks.Unlock()
	go runGPTAccountStatusCheck(job)
	return &cloned, nil
}

func GetGPTAccountStatusCheck(identity AuthIdentity, jobID string) (GPTAccountStatusCheckView, error) {
	gptAccountStatusChecks.Lock()
	defer gptAccountStatusChecks.Unlock()
	job, ok := gptAccountStatusChecks.jobs[jobID]
	if !ok || !sameGPTAccountStatusCheckIdentity(job, identity) {
		return GPTAccountStatusCheckView{}, errors.New("not_found")
	}
	return cloneGPTAccountStatusCheckView(job.view), nil
}

func CancelGPTAccountStatusCheck(identity AuthIdentity, jobID string) error {
	gptAccountStatusChecks.Lock()
	defer gptAccountStatusChecks.Unlock()
	job, ok := gptAccountStatusChecks.jobs[jobID]
	if !ok || !sameGPTAccountStatusCheckIdentity(job, identity) {
		return errors.New("not_found")
	}
	if job.view.State == "completed" || job.view.State == "cancelled" {
		return nil
	}
	job.view.State = "cancelled"
	job.cancel()
	for i := range job.credentials {
		if job.view.Results[i].Status == "pending" || job.view.Results[i].Status == "running" {
			job.view.Results[i].Status = "cancelled"
			job.view.Completed++
			job.view.Cancelled++
		}
		job.credentials[i] = GPTAccountStatusCheckCredential{}
	}
	return nil
}

func sameGPTAccountStatusCheckIdentity(job *gptAccountStatusCheckJob, identity AuthIdentity) bool {
	if job.ownerID != identity.UserID {
		return false
	}
	return job.identity == identity
}

func cloneGPTAccountStatusCheckView(view GPTAccountStatusCheckView) GPTAccountStatusCheckView {
	view.Results = append([]GPTAccountStatusCheckResult(nil), view.Results...)
	if view.ModelTest != nil {
		copy := *view.ModelTest
		view.ModelTest = &copy
	}
	for i := range view.Results {
		if view.Results[i].ModelTest != nil {
			copy := *view.Results[i].ModelTest
			view.Results[i].ModelTest = &copy
		}
	}
	return view
}

func runGPTAccountStatusCheck(job *gptAccountStatusCheckJob) {
	defer close(job.done)
	defer job.cancel()
	gptAccountStatusChecks.Lock()
	if job.view.State == "pending" {
		job.view.State = "running"
	}
	gptAccountStatusChecks.Unlock()
	for index := range job.credentials {
		select {
		case gptAccountStatusChecks.sem <- struct{}{}:
		case <-job.ctx.Done():
			markGPTAccountStatusCheckCancelled(job, index)
			continue
		}
		if job.ctx.Err() != nil {
			<-gptAccountStatusChecks.sem
			markGPTAccountStatusCheckCancelled(job, index)
			continue
		}
		if ValidateStepUpIdentity(job.identity) != nil || !gptAccountOwnerActive(&model.GPTAccount{OwnerID: job.ownerID}) {
			<-gptAccountStatusChecks.sem
			markGPTAccountStatusCheckResult(job, index, GPTAccountStatusCheckResult{Status: "failed", ErrorCode: "authorization_invalid"})
			zeroGPTAccountStatusCheckCredential(job, index)
			continue
		}
		gptAccountStatusChecks.Lock()
		credential := job.credentials[index]
		if job.ctx.Err() == nil {
			job.view.Results[index].Status = "running"
		}
		gptAccountStatusChecks.Unlock()
		result := checkGPTAccountStatusCredential(job.ctx, credential, job.view.ModelTest, func() bool {
			return ValidateStepUpIdentity(job.identity) == nil && gptAccountOwnerActive(&model.GPTAccount{OwnerID: job.ownerID})
		})
		credential = GPTAccountStatusCheckCredential{}
		<-gptAccountStatusChecks.sem
		zeroGPTAccountStatusCheckCredential(job, index)
		if job.ctx.Err() != nil {
			result = GPTAccountStatusCheckResult{Status: "cancelled"}
		}
		common.SysLog(fmt.Sprintf("gpt_account_status_check owner=%d job=%s row=%d status=%s error=%s", job.ownerID, job.jobID, index+1, result.Status, result.ErrorCode))
		markGPTAccountStatusCheckResult(job, index, result)
	}
	gptAccountStatusChecks.Lock()
	defer gptAccountStatusChecks.Unlock()
	if job.view.State != "cancelled" {
		job.view.State = "completed"
	}
	if gptAccountStatusChecks.jobs[job.jobID] == job {
		job.maxRuntime.Reset(gptAccountStatusCheckTTL)
	}
}

func zeroGPTAccountStatusCheckCredential(job *gptAccountStatusCheckJob, index int) {
	gptAccountStatusChecks.Lock()
	if index >= 0 && index < len(job.credentials) {
		job.credentials[index] = GPTAccountStatusCheckCredential{}
	}
	gptAccountStatusChecks.Unlock()
}

func markGPTAccountStatusCheckCancelled(job *gptAccountStatusCheckJob, index int) {
	zeroGPTAccountStatusCheckCredential(job, index)
	gptAccountStatusChecks.Lock()
	if index < len(job.view.Results) && (job.view.Results[index].Status == "pending" || job.view.Results[index].Status == "running") {
		email := job.view.Results[index].Email
		job.view.Results[index] = GPTAccountStatusCheckResult{Email: email, Status: "cancelled"}
		job.view.Completed++
		job.view.Cancelled++
	}
	gptAccountStatusChecks.Unlock()
}

func markGPTAccountStatusCheckResult(job *gptAccountStatusCheckJob, index int, result GPTAccountStatusCheckResult) {
	gptAccountStatusChecks.Lock()
	defer gptAccountStatusChecks.Unlock()
	if index >= len(job.view.Results) || (job.view.Results[index].Status != "pending" && job.view.Results[index].Status != "running") {
		return
	}
	result.Email = job.view.Results[index].Email
	job.view.Results[index] = result
	job.view.Completed++
	switch result.Status {
	case "success":
		job.view.Success++
	case "needs_attention":
		job.view.NeedsAttention++
	case "cancelled":
		job.view.Cancelled++
	default:
		job.view.Failed++
	}
}

func maskGPTAccountStatusCheckEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	_, size := utf8.DecodeRuneInString(local)
	local = local[:size]
	return local + "***" + email[at:]
}

func expireGPTAccountStatusCheck(jobID string) {
	gptAccountStatusChecks.Lock()
	defer gptAccountStatusChecks.Unlock()
	job, ok := gptAccountStatusChecks.jobs[jobID]
	if !ok {
		return
	}
	job.cancel()
	job.maxRuntime.Stop()
	clear(job.credentials)
	delete(gptAccountStatusChecks.jobs, jobID)
}

func checkGPTAccountStatusCredential(ctx context.Context, credential GPTAccountStatusCheckCredential, modelTest *GPTAccountModelTestOptions, authorized func() bool) (result GPTAccountStatusCheckResult) {
	started := time.Now()
	result.Status, result.ErrorCode = "failed", "automation_unavailable"
	if modelTest != nil {
		result.ModelTest = &GPTAccountModelTestResult{Status: "skipped", Model: modelTest.Model, Evaluation: "unscored"}
	}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if result.LoginStatus == "" && result.Status != "cancelled" {
			result.LoginStatus = result.Status
		}
	}()
	workerURL, token, err := gptWorkerConfiguration()
	if err != nil {
		return result
	}
	path := "/check"
	if modelTest != nil {
		path = "/probe"
	}
	workerURL = strings.TrimSuffix(workerURL, "/login") + path
	flow, err := newGPTAccountStatusCheckAuthorization(credential.Email)
	if err != nil {
		result.ErrorCode = "authorization_failed"
		return result
	}
	defer func() { flow = gptAccountDiagnosticAuthorization{} }()
	payload := map[string]string{"authorization_url": flow.url, "state": flow.state, "email": credential.Email, "password": credential.Password, "totp_secret": credential.TOTPSecret, "totp_provider": "local"}
	body, err := common.Marshal(payload)
	clear(payload)
	credential.Password, credential.TOTPSecret = "", ""
	if err != nil {
		result.ErrorCode = "authorization_failed"
		return result
	}
	defer clear(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workerURL, bytes.NewReader(body))
	if err != nil {
		return result
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			result.Status, result.ErrorCode = "cancelled", ""
			return result
		}
		result.ErrorCode = "worker_unavailable"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		result.ErrorCode = "worker_busy"
		return result
	}
	var workerResult struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Code      string `json:"code"`
		State     string `json:"state"`
	}
	if resp.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(resp.Body, 32<<10), &workerResult) != nil {
		result.ErrorCode = "worker_unavailable"
		return result
	}
	// Credentials are no longer needed once the isolated login worker returns.
	clear(body)
	req.GetBody = nil
	if workerResult.Status != "success" {
		if workerResult.Status == "needs_attention" {
			result.Status = "needs_attention"
		}
		result.ErrorCode = safeGPTAccountStatusCheckErrorCode(workerResult.ErrorCode)
		return result
	}
	if modelTest == nil {
		result.Status, result.ErrorCode = "success", ""
		return result
	}
	if workerResult.State != flow.state || len(workerResult.Code) > 4096 || !gptAccountStatusCheckCodePattern.MatchString(workerResult.Code) {
		result.ErrorCode = "invalid_callback"
		return result
	}
	result.LoginStatus = "success"
	modelClient, err := getCodexOAuthHTTPClient("")
	if err != nil {
		result.ErrorCode = "authorization_failed"
		return result
	}
	modelClient.Timeout = 90 * time.Second
	probe := runGPTAccountModelTest(ctx, modelClient, *modelTest, credential.Email, workerResult.Code, flow.verifier, flow.nonce, authorized)
	workerResult.Code = ""
	result.ModelTest = &probe
	result.Status, result.ErrorCode = probe.Status, probe.ErrorCode
	return result
}

func safeGPTAccountStatusCheckErrorCode(code string) string {
	if code == "" {
		return "automation_unavailable"
	}
	switch code {
	case "invalid_credentials", "account_unavailable", "account_disabled", "captcha_required",
		"email_verification_required", "consent_required", "additional_verification_required", "login_timeout",
		"invalid_totp_code", "totp_secret_required", "worker_busy", "automation_unavailable", "authorization_invalid",
		"login_navigation_blocked", "login_navigation_failed", "login_screen_unrecognized", "invalid_callback",
		"email_submission_failed", "password_submission_failed", "totp_submission_failed", "email_step_timeout",
		"password_step_timeout", "totp_step_timeout", "workspace_selection_required", "password_not_challenged", "totp_not_challenged":
		return code
	default:
		return "automation_unavailable"
	}
}

type gptAccountDiagnosticAuthorization struct{ url, state, verifier, nonce string }

func newGPTAccountStatusCheckAuthorization(email string) (gptAccountDiagnosticAuthorization, error) {
	var flow gptAccountDiagnosticAuthorization
	for _, field := range []*string{&flow.verifier, &flow.state, &flow.nonce} {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return gptAccountDiagnosticAuthorization{}, errors.New("authorization_failed")
		}
		*field = base64.RawURLEncoding.EncodeToString(random)
		clear(random)
	}
	challengeBytes := sha256.Sum256([]byte(flow.verifier))
	params := url.Values{"response_type": {"code"}, "client_id": {codexOAuthClientID}, "redirect_uri": {gptOAuthRedirect}, "scope": {"openid profile email"}, "state": {flow.state}, "nonce": {flow.nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])}, "code_challenge_method": {"S256"}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "originator": {"codex_cli_rs"}, "login_hint": {email}}
	flow.url = "https://auth.openai.com/oauth/authorize?" + params.Encode()
	return flow, nil
}
