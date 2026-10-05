package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const gptOAuthRedirect = "http://localhost:1455/auth/callback"
const gptOAuthPurpose = "gpt_account_oauth"

type GPTAccountAuthorization struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        int64  `json:"expires_at"`
}

type gptAccountOAuthFlow struct {
	AccountID      int          `json:"account_id"`
	Generation     int64        `json:"generation"`
	LeaseID        string       `json:"lease_id"`
	ChannelKeyHash string       `json:"channel_key_hash"`
	Identity       AuthIdentity `json:"identity"`
	Verifier       string       `json:"verifier"`
	Nonce          string       `json:"nonce"`
}

func gptWorkerConfiguration() (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(os.Getenv("GPT_ACCOUNT_WORKER_URL")))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("automation_unavailable")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return "", "", errors.New("automation_unavailable")
	}
	path := os.Getenv("GPT_ACCOUNT_WORKER_TOKEN_FILE")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("automation_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) < 32 {
		return "", "", errors.New("automation_unavailable")
	}
	return strings.TrimRight(u.String(), "/") + "/login", strings.TrimSpace(string(raw)), nil
}

func GPTAccountAutomationAvailable() bool { _, _, err := gptWorkerConfiguration(); return err == nil }

func AcquireGPTAccountJob(accountID int, automatic bool) (*model.GPTAccount, error) {
	return acquireGPTAccountJob(accountID, automatic, automatic)
}

func acquireGPTAccountJob(accountID int, automatic, requireAutoRelogin bool) (*model.GPTAccount, error) {
	now := time.Now().Unix()
	lease := uuid.NewString()
	query := model.DB.Model(&model.GPTAccount{}).Where("id = ? AND lease_until <= ?", accountID, now)
	if automatic {
		query = query.Where("next_retry_at <= ? AND failure_count < ? AND status NOT IN ?", now, 3, []string{"needs_attention", "logging_in"})
	}
	if requireAutoRelogin {
		query = query.Where("auto_relogin = ?", true)
	}
	result := query.Updates(map[string]any{"lease_id": lease, "lease_until": now + 600, "last_attempt_at": now, "next_retry_at": now + 900, "status": "logging_in", "last_error_code": "", "updated_at": now})
	if result.Error != nil {
		return nil, errors.New("database_error")
	}
	if result.RowsAffected != 1 {
		return nil, errors.New("account_busy")
	}
	var account model.GPTAccount
	if model.DB.First(&account, accountID).Error != nil {
		return nil, errors.New("account_changed")
	}
	if owner, err := model.GetUserById(account.OwnerID, false); err == nil {
		account.OwnerRole = owner.Role
	}
	return &account, nil
}

func ReleaseGPTAccountJob(account *model.GPTAccount, code string) {
	releaseGPTAccountJob(account, code, true)
}

func releaseGPTAccountJob(account *model.GPTAccount, code string, countFailure bool) {
	code = GPTAccountErrorCode(errors.New(code))
	if code == "operation_failed" {
		code = "authorization_failed"
	}
	updates := map[string]any{"lease_id": "", "lease_until": 0, "updated_at": time.Now().Unix()}
	if code != "" {
		status := "error"
		switch code {
		case "needs_attention", "identity_mismatch", "identity_verification_failed", "credentials_required", "automation_unavailable", "encryption_unavailable", "account_changed", "captcha_required", "email_verification_required", "consent_required", "additional_verification_required", "invalid_credentials", "account_unavailable", "account_disabled", "login_timeout", "invalid_callback", "authorization_invalid", "authorization_forbidden":
			status = "needs_attention"
		case "login_navigation_blocked", "login_screen_unrecognized", "totp_secret_required", "totp_provider_invalid_response", "invalid_totp_code":
			status = "needs_attention"
		case "invalid_gateway", "gateway_keys_required", "gateway_not_found", "gateway_immutable", "gateway_import_uncertain", "gateway_remote_conflict", "gateway_remote_missing", "gateway_credentials_required":
			status = "needs_attention"
		}
		if code == "worker_busy" {
			countFailure = false
		}
		if countFailure {
			updates["failure_count"] = gorm.Expr("failure_count + 1")
			if account.FailureCount >= 2 {
				status = "needs_attention"
			}
		} else if code != "worker_busy" {
			// A refresh-only failure did not attempt browser authorization. Do
			// not consume that budget or delay recovery of a later genuine401.
			updates["next_retry_at"] = 0
		}
		updates["status"], updates["last_error_code"] = status, code
	}
	model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_id = ?", account.ID, account.Generation, account.LeaseID).Updates(updates)
	model.RecordAuditLog(nil, model.AuditLog{UserId: account.OwnerID, ActorRole: account.OwnerRole, Category: model.AuditCategorySecurity, Action: "gpt_account.authorization", Content: "GPT account authorization", Success: code == "", Other: model.AuditOther{Op: &model.AuditOperation{Action: "gpt_account.authorization", Params: model.AuditFields{"account_id": account.ID, "error_code": code}}}})
}

func newGPTAccountOAuth(account *model.GPTAccount, identity AuthIdentity) (*GPTAccountAuthorization, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, errors.New("authorization_failed")
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challenge := sha256.Sum256([]byte(verifier))
	sealed, err := encryptGPTSecret(verifier, "gpt-oauth:"+strconv.Itoa(account.ID))
	if err != nil {
		return nil, err
	}
	flow := gptAccountOAuthFlow{AccountID: account.ID, Generation: account.Generation, LeaseID: account.LeaseID, ChannelKeyHash: account.ChannelKeyHash, Identity: identity, Verifier: sealed, Nonce: uuid.NewString()}
	payload, err := common.Marshal(flow)
	if err != nil {
		return nil, errors.New("authorization_failed")
	}
	expires := time.Now().Add(10 * time.Minute)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: gptOAuthPurpose, UserId: identity.UserID, SessionId: identity.SessionID, Payload: string(payload), ExpiresAt: expires})
	if err != nil {
		return nil, errors.New("authorization_failed")
	}
	params := url.Values{"response_type": {"code"}, "client_id": {codexOAuthClientID}, "redirect_uri": {gptOAuthRedirect}, "scope": {"openid profile email offline_access"}, "state": {state}, "nonce": {flow.Nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "originator": {"codex_cli_rs"}, "login_hint": {account.Email}}
	return &GPTAccountAuthorization{AuthorizationURL: "https://auth.openai.com/oauth/authorize?" + params.Encode(), ExpiresAt: expires.Unix()}, nil
}

func StartGPTAccountAuthorization(accountID int, identity AuthIdentity) (*GPTAccountAuthorization, error) {
	account, err := AcquireGPTAccountJob(accountID, false)
	if err != nil {
		return nil, err
	}
	result, err := newGPTAccountOAuth(account, identity)
	if err != nil {
		ReleaseGPTAccountJob(account, "authorization_failed")
	}
	return result, err
}

func FinishGPTAccountAuthorization(ctx context.Context, accountID int, identity AuthIdentity, redirectURL string) error {
	u, err := url.Parse(redirectURL)
	if err != nil || u.Scheme != "http" || u.Host != "localhost:1455" || u.Path != "/auth/callback" || u.User != nil || u.Fragment != "" {
		return errors.New("authorization_invalid")
	}
	query := u.Query()
	if len(query["state"]) != 1 || len(query["code"]) != 1 || query.Get("state") == "" || query.Get("code") == "" || len(query.Get("code")) > 8192 {
		return errors.New("authorization_invalid")
	}
	return finishGPTAccountOAuth(ctx, accountID, identity, query.Get("state"), query.Get("code"))
}

func finishGPTAccountOAuth(ctx context.Context, accountID int, identity AuthIdentity, state, code string) (resultErr error) {
	flow, err := model.ConsumeAuthFlow(state, model.AuthFlowMatch{Purpose: gptOAuthPurpose, UserId: identity.UserID, SessionId: identity.SessionID})
	if err != nil {
		return errors.New("authorization_invalid")
	}
	var payload gptAccountOAuthFlow
	if common.UnmarshalJsonStr(flow.Payload, &payload) != nil || payload.AccountID != accountID || payload.Identity != identity {
		return errors.New("authorization_invalid")
	}
	var account model.GPTAccount
	if model.DB.First(&account, accountID).Error != nil || account.Generation != payload.Generation || account.LeaseID != payload.LeaseID || account.LeaseUntil <= time.Now().Unix() {
		return errors.New("account_changed")
	}
	defer func() {
		if resultErr != nil {
			ReleaseGPTAccountJob(&account, resultErr.Error())
		} else {
			ReleaseGPTAccountJob(&account, "")
		}
	}()
	if identity.SessionID != "" {
		if ValidateStepUpIdentity(identity) != nil {
			return errors.New("authorization_invalid")
		}
	}
	if !gptAccountOwnerActive(&account) {
		return errors.New("authorization_forbidden")
	}
	owner, err := model.GetUserById(account.OwnerID, false)
	if err != nil {
		return errors.New("authorization_forbidden")
	}
	account.OwnerRole = owner.Role
	verifier, err := decryptGPTSecret(payload.Verifier, "gpt-oauth:"+strconv.Itoa(account.ID))
	if err != nil {
		return err
	}
	client, err := getCodexOAuthHTTPClient("")
	if err != nil {
		return errors.New("authorization_failed")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {codexOAuthClientID}, "code": {code}, "redirect_uri": {gptOAuthRedirect}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("authorization_failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("authorization_failed")
	}
	defer resp.Body.Close()
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(resp.Body, 128<<10), &tokens) != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 366*24*3600 {
		return errors.New("authorization_failed")
	}
	// Obtain issuer metadata and verify signature, audience, expiry and nonce.
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), "https://auth.openai.com")
	if err != nil {
		return errors.New("identity_verification_failed")
	}
	verified, err := provider.Verifier(&oidc.Config{ClientID: codexOAuthClientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}}).Verify(oidc.ClientContext(ctx, client), tokens.IDToken)
	if err != nil || verified.Nonce != payload.Nonce || verified.Subject == "" || len(verified.Subject) > 255 {
		return errors.New("identity_verification_failed")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Auth          struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if verified.Claims(&claims) != nil || !claims.EmailVerified || !strings.EqualFold(strings.TrimSpace(claims.Email), account.Email) || claims.Auth.AccountID == "" {
		return errors.New("identity_mismatch")
	}
	if account.AccountID != "" && account.AccountID != claims.Auth.AccountID {
		return errors.New("identity_mismatch")
	}
	if account.OAuthSubject != "" && account.OAuthSubject != verified.Subject {
		return errors.New("identity_mismatch")
	}
	if account.ChannelID != nil {
		channel, err := model.GetChannelById(*account.ChannelID, true)
		if err != nil {
			return errors.New("account_changed")
		}
		existing, err := parseCodexOAuthKey(channel.Key)
		if err != nil || existing.AccountID != claims.Auth.AccountID || (existing.Email != "" && !strings.EqualFold(existing.Email, account.Email)) {
			return errors.New("identity_mismatch")
		}
	}
	if identity.SessionID != "" && ValidateStepUpIdentity(identity) != nil {
		return errors.New("authorization_invalid")
	}
	if !gptAccountOwnerActive(&account) {
		return errors.New("authorization_forbidden")
	}
	key := CodexOAuthKey{IDToken: tokens.IDToken, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccountID: claims.Auth.AccountID, Email: account.Email, Type: "codex", LastRefresh: time.Now().Format(time.RFC3339), Expired: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Format(time.RFC3339)}
	if account.GatewayID != nil {
		account.OAuthSubject = verified.Subject
		return StoreVerifiedGPTAccountGatewayCredential(ctx, &account, key)
	}
	encoded, err := common.Marshal(key)
	if err != nil {
		return errors.New("authorization_failed")
	}
	account.ChannelKeyHash = payload.ChannelKeyHash
	account.OAuthSubject = verified.Subject
	if err := model.StoreGPTAccountCredential(&account, string(encoded), claims.Auth.AccountID); err != nil {
		return errors.New("account_changed")
	}
	model.InitChannelCache()
	return nil
}

func gptAccountOwnerActive(account *model.GPTAccount) bool {
	owner, err := model.GetUserById(account.OwnerID, false)
	return err == nil && owner.Status == common.UserStatusEnabled && owner.Role >= common.RoleAdminUser && authz.Can(owner.Id, owner.Role, authz.GPTAccountManage)
}

func StartGPTAccountLogin(accountID int, identity AuthIdentity, automatic bool) error {
	if !GPTAccountAutomationAvailable() {
		model.DB.Model(&model.GPTAccount{}).Where("id = ?", accountID).Updates(map[string]any{"status": "needs_attention", "last_error_code": "automation_unavailable"})
		return errors.New("automation_unavailable")
	}
	account, err := AcquireGPTAccountJob(accountID, automatic)
	if err != nil {
		return err
	}
	go runGPTAccountLogin(account, identity, false)
	return nil
}

func runGPTAccountLogin(account *model.GPTAccount, identity AuthIdentity, refreshFirst bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if !gptAccountOwnerActive(account) {
		ReleaseGPTAccountJob(account, "authorization_forbidden")
		return
	}
	if refreshFirst && account.ChannelID != nil {
		if _, _, err := refreshManagedGPTAccountCredential(ctx, account); err == nil {
			ReleaseGPTAccountJob(account, "")
			return
		} else if errors.Is(err, model.ErrGPTAccountStale) {
			ReleaseGPTAccountJob(account, "account_changed")
			return
		}
	}
	secrets, err := DecryptGPTAccountSecrets(account)
	if err != nil {
		ReleaseGPTAccountJob(account, "encryption_unavailable")
		return
	}
	if secrets.Password == "" {
		ReleaseGPTAccountJob(account, "credentials_required")
		return
	}
	authorization, err := newGPTAccountOAuth(account, identity)
	if err != nil {
		ReleaseGPTAccountJob(account, "authorization_failed")
		return
	}
	authURL, _ := url.Parse(authorization.AuthorizationURL)
	state := authURL.Query().Get("state")
	workerURL, token, err := gptWorkerConfiguration()
	if err != nil {
		ReleaseGPTAccountJob(account, "automation_unavailable")
		return
	}
	if secrets.TOTPProvider == "" {
		secrets.TOTPProvider = "local"
	}
	body, err := common.Marshal(map[string]string{"authorization_url": authorization.AuthorizationURL, "state": state, "email": account.Email, "password": secrets.Password, "totp_secret": secrets.TOTPSecret, "totp_provider": secrets.TOTPProvider})
	if err != nil {
		ReleaseGPTAccountJob(account, "authorization_failed")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workerURL, strings.NewReader(string(body)))
	if err != nil {
		ReleaseGPTAccountJob(account, "automation_unavailable")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		ReleaseGPTAccountJob(account, "worker_unavailable")
		return
	}
	defer resp.Body.Close()
	var result struct {
		Status    string `json:"status"`
		Code      string `json:"code"`
		State     string `json:"state"`
		ErrorCode string `json:"error_code"`
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		ReleaseGPTAccountJob(account, "worker_busy")
		return
	}
	if resp.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(resp.Body, 32<<10), &result) != nil {
		ReleaseGPTAccountJob(account, "worker_unavailable")
		return
	}
	if result.Status != "success" {
		code := GPTAccountErrorCode(errors.New(result.ErrorCode))
		if code == "operation_failed" {
			code = "needs_attention"
		}
		ReleaseGPTAccountJob(account, code)
		return
	}
	if result.State != state || result.Code == "" || len(result.Code) > 8192 {
		ReleaseGPTAccountJob(account, "authorization_invalid")
		return
	}
	_ = finishGPTAccountOAuth(ctx, account.ID, identity, state, result.Code)
}

// ScheduleGPTAccountRecovery is called on an actual Codex HTTP 401 before any
// status-code mappings. A true result also covers stale or concurrent failures:
// ordinary auto-ban must not disable a newer key or overwrite our pause reason.
// Recovery serves subsequent requests, never replays work.
func ScheduleGPTAccountRecovery(channelID int, failedKey string) bool {
	var account model.GPTAccount
	if model.DB.Where("channel_id = ? AND auto_relogin = ?", channelID, true).First(&account).Error != nil || !gptAccountOwnerActive(&account) {
		return false
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil || channel.Status == common.ChannelStatusManuallyDisabled || channel.Type != constant.ChannelTypeCodex || channel.ChannelInfo.IsMultiKey {
		return false
	}
	if model.GPTChannelKeyHash(failedKey) != model.GPTChannelKeyHash(channel.Key) {
		return true
	}
	if account.ChannelKeyHash != model.GPTChannelKeyHash(channel.Key) {
		return false
	}
	claimed, err := AcquireGPTAccountJob(account.ID, true)
	if err != nil {
		return true
	}
	if model.PauseGPTAccountChannel(claimed) != nil {
		ReleaseGPTAccountJob(claimed, "account_changed")
		return true
	}
	model.InitChannelCache()
	go runGPTAccountLogin(claimed, AuthIdentity{UserID: account.OwnerID}, true)
	return true
}

// ScheduleGPTAccountRecoveries resumes transient recovery failures after the
// persisted cooldown. Browser jobs that were abandoned require manual action.
func ScheduleGPTAccountRecoveries() {
	now := time.Now().Unix()
	var accounts []model.GPTAccount
	if model.DB.Where("auto_relogin = ? AND status = ? AND failure_count < ? AND lease_until <= ? AND next_retry_at <= ?", true, "error", 3, now, now).Order("next_retry_at ASC, id ASC").Limit(100).Find(&accounts).Error != nil {
		return
	}
	for _, account := range accounts {
		if account.ChannelID == nil {
			continue
		}
		channel, err := model.GetChannelById(*account.ChannelID, true)
		if err != nil || channel.Status != common.ChannelStatusAutoDisabled || channel.GetOtherInfo()["status_reason"] != model.GPTAccountRecoveryReason || account.ChannelKeyHash != model.GPTChannelKeyHash(channel.Key) {
			continue
		}
		ScheduleGPTAccountRecovery(channel.Id, channel.Key)
	}
}

func refreshManagedGPTAccountCredential(ctx context.Context, account *model.GPTAccount) (*CodexOAuthKey, *model.Channel, error) {
	if account.GatewayID != nil {
		return nil, nil, RefreshManagedGPTAccountGateway(ctx, account)
	}
	if account.ChannelID == nil {
		return nil, nil, errors.New("channel_required")
	}
	channel, err := model.GetChannelById(*account.ChannelID, true)
	if err != nil || channel.Type != constant.ChannelTypeCodex || channel.ChannelInfo.IsMultiKey || channel.Status == common.ChannelStatusManuallyDisabled || model.GPTChannelKeyHash(channel.Key) != account.ChannelKeyHash {
		return nil, nil, model.ErrGPTAccountStale
	}
	key, err := parseCodexOAuthKey(channel.Key)
	if err != nil || key.RefreshToken == "" {
		return nil, nil, errors.New("refresh_failed")
	}
	result, err := RefreshCodexOAuthTokenWithProxy(ctx, key.RefreshToken, channel.GetSetting().Proxy)
	if err != nil {
		return nil, nil, errors.New("refresh_failed")
	}
	if id, ok := ExtractCodexAccountIDFromJWT(result.AccessToken); !ok || id != key.AccountID {
		return nil, nil, errors.New("identity_mismatch")
	}
	key.AccessToken, key.RefreshToken, key.LastRefresh, key.Expired = result.AccessToken, result.RefreshToken, time.Now().Format(time.RFC3339), result.ExpiresAt.Format(time.RFC3339)
	encoded, err := common.Marshal(key)
	if err != nil {
		return nil, nil, errors.New("refresh_failed")
	}
	if model.StoreGPTAccountCredential(account, string(encoded), key.AccountID) != nil {
		return nil, nil, model.ErrGPTAccountStale
	}
	model.InitChannelCache()
	return key, channel, nil
}

func RefreshManagedGPTAccount(ctx context.Context, accountID int) (*CodexOAuthKey, *model.Channel, error) {
	return refreshGPTAccountCredential(ctx, accountID, false)
}

func refreshGPTAccountCredential(ctx context.Context, accountID int, automatic bool) (*CodexOAuthKey, *model.Channel, error) {
	account, err := acquireGPTAccountJob(accountID, automatic, false)
	if err != nil {
		return nil, nil, err
	}
	if !gptAccountOwnerActive(account) {
		ReleaseGPTAccountJob(account, "authorization_forbidden")
		return nil, nil, errors.New("authorization_forbidden")
	}
	key, channel, err := refreshManagedGPTAccountCredential(ctx, account)
	code := ""
	if err != nil {
		code = GPTAccountErrorCode(err)
	}
	releaseGPTAccountJob(account, code, false)
	return key, channel, err
}

func GPTAccountErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch err.Error() {
	case "invalid_gateway", "gateway_keys_required", "gateway_not_found", "gateway_immutable", "gateway_unavailable", "gateway_import_failed", "gateway_import_uncertain", "gateway_remote_conflict", "gateway_remote_missing", "gateway_credentials_required":
		return err.Error()
	case "login_navigation_blocked", "login_navigation_failed", "email_submission_failed", "password_submission_failed", "totp_submission_failed", "email_step_timeout", "password_step_timeout", "totp_step_timeout", "login_screen_unrecognized", "totp_secret_required", "totp_provider_unavailable", "totp_provider_timeout", "totp_provider_invalid_response", "invalid_totp_code":
		return err.Error()
	case "", "encryption_unavailable", "automation_unavailable", "account_busy", "account_changed", "authorization_invalid", "authorization_failed", "identity_verification_failed", "identity_mismatch", "credentials_required", "worker_unavailable", "worker_busy", "needs_attention", "refresh_failed", "channel_required", "authorization_forbidden", "captcha_required", "email_verification_required", "consent_required", "additional_verification_required", "invalid_credentials", "account_unavailable", "account_disabled", "login_timeout", "invalid_callback":
		return err.Error()
	default:
		return "operation_failed"
	}
}
