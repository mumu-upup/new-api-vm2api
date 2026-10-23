package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const maxGPTAccountOAuthImportBytes = 32 << 10

// GPTAccountOAuthImportInput contains operator-supplied OAuth JSON and the
// metadata used only when a matching managed account does not exist.
type GPTAccountOAuthImportInput struct {
	OAuthJSON   string
	Name        string
	Group       string
	Models      string
	AutoRelogin *bool
	GatewayID   *int
}

// ParseCodexOAuthImport validates the shape and lifetime of an operator-supplied
// Codex OAuth bundle. Historical ID-token claims are unverified account-binding
// metadata, not proof of authentication, and may outlive that token's expiry.
// Access JWT claims are checked for consistency and expiry, not verified here.
// The caller must pass the administrator security-verification flow.
func ParseCodexOAuthImport(raw string) (CodexOAuthKey, error) {
	if len(raw) == 0 || len(raw) > maxGPTAccountOAuthImportBytes {
		return CodexOAuthKey{}, errors.New("oauth_import_invalid")
	}
	var key CodexOAuthKey
	if common.Unmarshal([]byte(raw), &key) != nil {
		return CodexOAuthKey{}, errors.New("oauth_import_invalid")
	}
	key.Email = model.NormalizeEmail(key.Email)
	key.Type = strings.ToLower(strings.TrimSpace(key.Type))
	if key.Type == "" {
		key.Type = "codex"
	}
	if key.Type != "codex" || key.AccessToken == "" || key.RefreshToken == "" || key.IDToken == "" || key.AccountID == "" || key.Email == "" || len(key.AccessToken) > 8192 || len(key.RefreshToken) > 8192 || len(key.IDToken) > 8192 || len(key.AccountID) > 128 || len(key.Email) > 254 || common.Validate.Var(key.Email, "required,email") != nil {
		return CodexOAuthKey{}, errors.New("oauth_import_invalid")
	}
	expired, err := time.Parse(time.RFC3339, strings.TrimSpace(key.Expired))
	if err != nil || !expired.After(time.Now()) {
		return CodexOAuthKey{}, errors.New("oauth_import_invalid")
	}
	claims, err := parseCodexOAuthClaims(key.IDToken)
	if err != nil || !claims.EmailVerified || !strings.EqualFold(strings.TrimSpace(claims.Email), key.Email) || claims.AccountID != key.AccountID {
		return CodexOAuthKey{}, errors.New("oauth_import_invalid")
	}
	if accessClaims, ok := parseCodexAccessClaims(key.AccessToken); ok {
		if !accessClaims.Valid || accessClaims.AccountID != key.AccountID || !strings.EqualFold(accessClaims.Email, key.Email) || accessClaims.Subject != claims.Subject {
			return CodexOAuthKey{}, errors.New("oauth_import_invalid")
		}
		if expired.Unix() > accessClaims.ExpiresAt {
			key.Expired = time.Unix(accessClaims.ExpiresAt, 0).Format(time.RFC3339)
		}
	}
	return key, nil
}

type codexOAuthClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	AccountID     string
}

func parseCodexOAuthClaims(raw string) (codexOAuthClaims, error) {
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"})).ParseUnverified(raw, claims)
	if err != nil || parsed.Method.Alg() != "RS256" {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	issuer, ok := claims["iss"].(string)
	if !ok || issuer != "https://auth.openai.com" {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	audience, err := claims.GetAudience()
	if err != nil || len(audience) != 1 || audience[0] != codexOAuthClientID {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	subject, ok := claims["sub"].(string)
	if !ok || strings.TrimSpace(subject) == "" || len(subject) > 255 {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	expires, ok := claims["exp"].(float64)
	if !ok || !validUnixClaim(expires) {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	email, ok := claims["email"].(string)
	if !ok {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	emailVerified, ok := claims["email_verified"].(bool)
	if !ok {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	auth, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	accountID, ok := auth["chatgpt_account_id"].(string)
	if !ok || strings.TrimSpace(accountID) == "" {
		return codexOAuthClaims{}, errors.New("oauth_import_invalid")
	}
	return codexOAuthClaims{Subject: strings.TrimSpace(subject), Email: email, EmailVerified: emailVerified, AccountID: strings.TrimSpace(accountID)}, nil
}

type codexAccessClaims struct {
	Valid     bool
	Subject   string
	Email     string
	AccountID string
	ExpiresAt int64
}

// parseCodexAccessClaims validates claims only when an access token has the
// JWT shape. Opaque access tokens remain compatible with existing channels;
// this parser is still deliberately unverified because imports are gated by a
// server-side administrator proof and this token is not an authentication
// assertion for the gateway.
func parseCodexAccessClaims(raw string) (codexAccessClaims, bool) {
	if strings.Count(raw, ".") != 2 {
		return codexAccessClaims{}, false
	}
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"})).ParseUnverified(raw, claims)
	if err != nil || parsed.Method.Alg() != "RS256" {
		return codexAccessClaims{}, true
	}
	issuer, issuerOK := claims["iss"].(string)
	if !issuerOK || issuer != "https://auth.openai.com" {
		return codexAccessClaims{}, true
	}
	audience, err := claims.GetAudience()
	if err != nil || len(audience) != 1 || audience[0] != "https://api.openai.com/v1" {
		return codexAccessClaims{}, true
	}
	expires, expiresOK := claims["exp"].(float64)
	if !expiresOK || !validUnixClaim(expires) || int64(expires) <= time.Now().Unix() {
		return codexAccessClaims{}, true
	}
	if notBefore, ok := claims["nbf"]; ok {
		value, valueOK := notBefore.(float64)
		if !valueOK || !validUnixClaim(value) || int64(value) > time.Now().Unix() {
			return codexAccessClaims{}, true
		}
	}
	subject, subjectOK := claims["sub"].(string)
	auth, authOK := claims[codexJWTClaimPath].(map[string]any)
	accountID, accountOK := auth["chatgpt_account_id"].(string)
	profile, profileOK := claims["https://api.openai.com/profile"].(map[string]any)
	email, emailOK := profile["email"].(string)
	if !subjectOK || strings.TrimSpace(subject) == "" || !authOK || !accountOK || strings.TrimSpace(accountID) == "" || !profileOK || !emailOK || strings.TrimSpace(email) == "" {
		return codexAccessClaims{}, true
	}
	if rootEmail, ok := claims["email"]; ok {
		value, valueOK := rootEmail.(string)
		if !valueOK || !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(email)) {
			return codexAccessClaims{}, true
		}
	}
	return codexAccessClaims{Valid: true, Subject: strings.TrimSpace(subject), Email: strings.TrimSpace(email), AccountID: strings.TrimSpace(accountID), ExpiresAt: int64(expires)}, true
}

func validUnixClaim(value float64) bool {
	return value > 0 && value <= 253402300799 && value == math.Trunc(value)
}

func parseCodexOAuthSubject(raw string) (string, error) {
	claims, err := parseCodexOAuthClaims(raw)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}

// ReclaimExpiredGPTAccountJobs turns only expired browser leases into a
// persistent, manually retryable state. The conditional update is idempotent
// and cannot take over a still-running worker.
func ReclaimExpiredGPTAccountJobs(now int64) (int64, error) {
	result := model.DB.Model(&model.GPTAccount{}).
		Where("status = ? AND lease_until <= ?", "logging_in", now).
		Updates(map[string]any{
			"status":          "needs_attention",
			"last_error_code": "login_timeout",
			"lease_id":        "",
			"lease_until":     0,
			"next_retry_at":   0,
			"updated_at":      now,
		})
	return result.RowsAffected, result.Error
}

// ImportGPTAccountOAuth creates or updates one managed Codex account and its
// channel without starting the browser worker. accountID may be zero to match
// an existing account by email or create a new account for the operator.
func ImportGPTAccountOAuth(ctx context.Context, ownerID, accountID int, input GPTAccountOAuthImportInput) (*model.GPTAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := ParseCodexOAuthImport(input.OAuthJSON)
	if err != nil {
		return nil, err
	}
	subject, err := parseCodexOAuthSubject(key.IDToken)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = key.Email
	}
	group, err := model.ResolveGPTAccountGroup(input.Group)
	if err != nil {
		return nil, err
	}
	models, err := model.ResolveGPTAccountModels(input.Models)
	if err != nil {
		return nil, err
	}
	if len(name) > 128 || len(group) > 64 || strings.ContainsAny(group, "\r\n") || len(models) > 8192 || strings.ContainsAny(models, "\r\n") {
		return nil, errors.New("invalid_account")
	}
	if !gptAccountOwnerActive(&model.GPTAccount{OwnerID: ownerID}) {
		return nil, errors.New("authorization_forbidden")
	}
	encoded, err := common.Marshal(key)
	if err != nil {
		return nil, errors.New("authorization_failed")
	}

	var account model.GPTAccount
	created := false
	if accountID > 0 {
		if model.DB.Where("id = ? AND owner_id = ?", accountID, ownerID).First(&account).Error != nil {
			return nil, errors.New("account_not_found")
		}
	} else {
		account = model.GPTAccount{OwnerID: ownerID, Name: name, Email: key.Email, Group: group, Models: models, SecretID: uuid.NewString(), Generation: 1, Status: "pending", AutoRelogin: true, OAuthSubject: subject}
		if input.AutoRelogin != nil {
			account.AutoRelogin = *input.AutoRelogin
		}
		gatewayID := input.GatewayID
		if gatewayID == nil {
			gatewayID, err = model.ResolveGPTAccountDefaultGateway()
			if err != nil {
				return nil, err
			}
		}
		if gatewayID != nil {
			if err := ApplyGPTAccountGateway(&account, gatewayID); err != nil {
				return nil, err
			}
		}
		if err := EncryptGPTAccountSecrets(&account, GPTAccountSecrets{}); err != nil {
			return nil, err
		}
		resolved, isCreated, err := model.CreateGPTAccountCredential(&account, string(encoded), key.AccountID)
		if err != nil {
			if errors.Is(err, model.ErrGPTAccountImportConflict) {
				return nil, errors.New("account_conflict")
			}
			return nil, err
		}
		account = *resolved
		created = isCreated
		if created && account.GatewayID == nil {
			return &account, nil
		}
	}
	if model.NormalizeGPTAccountProtocol(account.Protocol) != model.GPTAccountProtocolCodex {
		return nil, errors.New("identity_mismatch")
	}
	if account.Email != key.Email || (account.AccountID != "" && account.AccountID != key.AccountID) || (account.OAuthSubject != "" && account.OAuthSubject != subject) {
		if created {
			_ = model.DB.Delete(&account).Error
		}
		return nil, errors.New("identity_mismatch")
	}
	if account.GatewayID != nil {
		if input.GatewayID != nil && *input.GatewayID != *account.GatewayID {
			if created {
				_ = model.DB.Delete(&account).Error
			}
			return nil, errors.New("gateway_immutable")
		}
		job, err := AcquireGPTAccountJob(account.ID, false)
		if err != nil {
			if created {
				_ = model.DB.Delete(&account).Error
			}
			return nil, err
		}
		job.OAuthSubject = subject
		err = StoreVerifiedGPTAccountGatewayCredential(ctx, job, key)
		ReleaseGPTAccountJob(job, GPTAccountErrorCode(err))
		if err != nil {
			return nil, err
		}
		if model.DB.First(&account, account.ID).Error != nil {
			return nil, errors.New("account_changed")
		}
		return &account, nil
	}
	if input.GatewayID != nil {
		if created {
			_ = model.DB.Delete(&account).Error
		}
		return nil, errors.New("gateway_immutable")
	}
	job, err := AcquireGPTAccountJob(account.ID, false)
	if err != nil {
		if created {
			_ = model.DB.Delete(&account).Error
		}
		return nil, err
	}
	job.OAuthSubject = subject
	err = model.StoreGPTAccountCredential(job, string(encoded), key.AccountID)
	if err != nil {
		code := GPTAccountErrorCode(err)
		if code == "operation_failed" {
			code = "authorization_failed"
		}
		ReleaseGPTAccountJob(job, code)
		if created {
			_ = model.DB.Delete(&account).Error
		}
		return nil, errors.New(code)
	}
	ReleaseGPTAccountJob(job, "")
	if model.DB.First(&account, account.ID).Error != nil {
		return nil, errors.New("account_changed")
	}
	return &account, nil
}
