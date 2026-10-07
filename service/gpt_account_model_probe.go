package service

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

const (
	gptModelProbeIssuer         = "https://auth.openai.com"
	gptModelProbeJWKSURL        = "https://auth.openai.com/.well-known/jwks.json"
	gptModelProbeResponseURL    = "https://chatgpt.com/backend-api/codex/responses"
	gptModelProbeMaxAnswerBytes = 32 << 10
	gptModelProbeMaxStreamBytes = 1 << 20
)

var errGPTModelProbeInvalidJSON = errors.New("response_invalid")

type GPTAccountModelTestOptions struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	ExpectedAnswer string `json:"expected_answer"`
}

type GPTAccountModelTestResult struct {
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	Model       string `json:"model"`
	ActualModel string `json:"actual_model,omitempty"`
	Answer      string `json:"answer,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	Evaluation  string `json:"evaluation"`
}

func ValidateGPTAccountModelTestOptions(options *GPTAccountModelTestOptions) error {
	if options == nil {
		return nil
	}
	if !validGPTModelProbeModel(options.Model) || !utf8.ValidString(options.Prompt) || strings.TrimSpace(options.Prompt) == "" || len(options.Prompt) > 4000 || !utf8.ValidString(options.ExpectedAnswer) || len(options.ExpectedAnswer) > 1000 {
		return errors.New("invalid_model_test")
	}
	return nil
}

func validGPTModelProbeModel(model string) bool {
	if len(model) == 0 || len(model) > 100 {
		return false
	}
	for i := range len(model) {
		c := model[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if i == 0 || !strings.ContainsRune("._:-", rune(c)) {
			return false
		}
	}
	return true
}

// runGPTAccountModelTest uses ephemeral credentials for exactly one model call.
// It never creates a channel, stores credentials, refreshes tokens, or bills the
// gateway. Authorization is rechecked at both credential-use boundaries.
func runGPTAccountModelTest(ctx context.Context, client *http.Client, options GPTAccountModelTestOptions, email, code, verifier, nonce string, authorized func() bool) (result GPTAccountModelTestResult) {
	var modelStarted time.Time
	result = GPTAccountModelTestResult{Status: "failed", Model: options.Model, Evaluation: "unscored"}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	defer func() {
		if !modelStarted.IsZero() {
			result.ElapsedMS = time.Since(modelStarted).Milliseconds()
		}
		code, verifier, nonce = "", "", ""
		if ctx.Err() != nil {
			result.ErrorCode = gptModelProbeNetworkError(ctx, ctx.Err())
			result.Status, result.Answer, result.Evaluation = "failed", "", "unscored"
			if result.ErrorCode == "cancelled" {
				result.Status = "cancelled"
			}
		}
	}()
	if ctx.Err() != nil {
		return result
	}
	if err := ValidateGPTAccountModelTestOptions(&options); err != nil {
		result.ErrorCode = "invalid_model_test"
		return result
	}
	if authorized == nil || !authorized() {
		result.ErrorCode = "authorization_invalid"
		return result
	}
	if client == nil || code == "" || len(code) > 8192 || len(verifier) < 43 || len(verifier) > 128 || nonce == "" || len(nonce) > 256 {
		result.ErrorCode = "authorization_failed"
		return result
	}
	// Never inherit a redirect policy or cookie jar from a caller. A redirect
	// could disclose a code or bearer token even to another path on the issuer.
	isolatedClient := *client
	isolatedClient.Jar = nil
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if isolatedClient.Timeout <= 0 || isolatedClient.Timeout > 90*time.Second {
		isolatedClient.Timeout = 90 * time.Second
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {codexOAuthClientID}, "code": {code}, "redirect_uri": {gptOAuthRedirect}, "code_verifier": {verifier}}
	encodedForm := []byte(form.Encode())
	clear(form)
	defer clear(encodedForm)
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(string(encodedForm)))
	if err != nil {
		result.ErrorCode = "authorization_failed"
		return result
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := isolatedClient.Do(tokenRequest)
	clear(encodedForm)
	tokenRequest.GetBody = nil
	code, verifier = "", ""
	if err != nil {
		result.ErrorCode = gptModelProbeNetworkError(ctx, err)
		return result
	}
	defer tokenResponse.Body.Close()
	// Refresh tokens are deliberately not represented or decoded here.
	var tokens struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	defer func() { tokens.AccessToken, tokens.IDToken = "", "" }()
	if tokenResponse.StatusCode != http.StatusOK {
		result.ErrorCode = "authorization_failed"
		return result
	}
	if err := readGPTModelProbeJSON(tokenResponse.Body, &tokens); err != nil {
		result.ErrorCode = "authorization_failed"
		if !errors.Is(err, errGPTModelProbeInvalidJSON) {
			result.ErrorCode = gptModelProbeNetworkError(ctx, err)
		}
		return result
	}
	if tokens.AccessToken == "" || len(tokens.AccessToken) > 8192 || tokens.IDToken == "" || len(tokens.IDToken) > 16384 || (tokens.TokenType != "" && !strings.EqualFold(tokens.TokenType, "Bearer")) || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 366*24*3600 {
		result.ErrorCode = "authorization_failed"
		return result
	}
	// Pin the published JWKS destination and perform its bounded fetch with the
	// job context. oidc.RemoteKeySet intentionally detaches background fetches
	// from caller cancellation, which is unsuitable for this one-shot operation.
	keyRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, gptModelProbeJWKSURL, nil)
	if err != nil {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	keyResponse, err := isolatedClient.Do(keyRequest)
	if err != nil {
		result.ErrorCode = gptModelProbeNetworkError(ctx, err)
		return result
	}
	defer keyResponse.Body.Close()
	var keySet jose.JSONWebKeySet
	if keyResponse.StatusCode != http.StatusOK {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	if err := readGPTModelProbeJSON(keyResponse.Body, &keySet); err != nil {
		result.ErrorCode = "identity_verification_failed"
		if !errors.Is(err, errGPTModelProbeInvalidJSON) {
			result.ErrorCode = gptModelProbeNetworkError(ctx, err)
		}
		return result
	}
	publicKeys := make([]crypto.PublicKey, 0, len(keySet.Keys))
	for _, key := range keySet.Keys {
		if !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
			continue
		}
		switch publicKey := key.Key.(type) {
		case *rsa.PublicKey:
			if (key.Algorithm == "" || key.Algorithm == oidc.RS256) && publicKey.N.BitLen() >= 2048 {
				publicKeys = append(publicKeys, publicKey)
			}
		case *ecdsa.PublicKey:
			if (key.Algorithm == "" || key.Algorithm == oidc.ES256) && publicKey.Curve == elliptic.P256() {
				publicKeys = append(publicKeys, publicKey)
			}
		}
	}
	idVerifier := oidc.NewVerifier(gptModelProbeIssuer, &oidc.StaticKeySet{PublicKeys: publicKeys}, &oidc.Config{ClientID: codexOAuthClientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}})
	verified, err := idVerifier.Verify(ctx, tokens.IDToken)
	if err != nil || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(nonce)) != 1 || strings.TrimSpace(verified.Subject) == "" || len(verified.Subject) > 255 {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	if verified.AccessTokenHash != "" && verified.VerifyAccessToken(tokens.AccessToken) != nil {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	var claims struct {
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		AuthorizedParty string `json:"azp"`
		Auth            struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if verified.Claims(&claims) != nil {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	if (len(verified.Audience) > 1 && claims.AuthorizedParty == "") || (claims.AuthorizedParty != "" && claims.AuthorizedParty != codexOAuthClientID) {
		result.ErrorCode = "identity_verification_failed"
		return result
	}
	if !claims.EmailVerified || strings.TrimSpace(email) == "" || !strings.EqualFold(strings.TrimSpace(claims.Email), strings.TrimSpace(email)) || claims.Auth.AccountID == "" || len(claims.Auth.AccountID) > 128 || strings.ContainsFunc(claims.Auth.AccountID, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		result.ErrorCode = "identity_mismatch"
		return result
	}
	// Token verification and account binding happen before any model request.
	tokens.IDToken, nonce = "", ""
	if ctx.Err() != nil {
		return result
	}
	if !authorized() {
		result.ErrorCode = "authorization_invalid"
		return result
	}
	requestBody, err := common.Marshal(map[string]any{
		"model": options.Model, "store": false, "stream": true,
		"instructions": "Answer the user's question directly and concisely. Do not use tools.",
		"input":        []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": options.Prompt}}}},
	})
	if err != nil {
		result.ErrorCode = "model_response_invalid"
		return result
	}
	defer clear(requestBody)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gptModelProbeResponseURL, strings.NewReader(string(requestBody)))
	if err != nil {
		result.ErrorCode = "model_response_invalid"
		return result
	}
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	defer request.Header.Del("Authorization")
	request.Header.Set("chatgpt-account-id", claims.Auth.AccountID)
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	request.Header.Set("originator", "codex_cli_rs")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	modelStarted = time.Now()
	response, err := isolatedClient.Do(request)
	tokens.AccessToken = ""
	clear(requestBody)
	request.GetBody = nil
	if err != nil {
		result.ErrorCode = gptModelProbeNetworkError(ctx, err)
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error gptModelProbeUpstreamError `json:"error"`
		}
		if err := readGPTModelProbeJSON(response.Body, &failure); err != nil && !errors.Is(err, errGPTModelProbeInvalidJSON) {
			result.ErrorCode = gptModelProbeNetworkError(ctx, err)
			return result
		}
		result.ErrorCode = classifyGPTModelProbeError(response.StatusCode, failure.Error)
		return result
	}
	result.Answer, result.ActualModel, result.ErrorCode = readGPTModelProbeStream(ctx, response.Body)
	if result.ErrorCode != "" {
		return result
	}
	result.Status = "success"
	expected := strings.TrimSpace(strings.ReplaceAll(options.ExpectedAnswer, "\r\n", "\n"))
	if expected != "" {
		result.Evaluation = "mismatch"
		if strings.TrimSpace(strings.ReplaceAll(result.Answer, "\r\n", "\n")) == expected {
			result.Evaluation = "matched"
		}
	}
	return result
}

// All auxiliary responses have the same strict limit; clear buffered OAuth
// secrets after decoding and reject truncated or concatenated JSON documents.
func readGPTModelProbeJSON(body io.Reader, value any) error {
	const limit = 128 << 10
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	defer clear(raw)
	if err != nil {
		return err
	}
	if len(raw) > limit || common.Unmarshal(raw, value) != nil {
		return errGPTModelProbeInvalidJSON
	}
	return nil
}

type gptModelProbeUpstreamError struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

func classifyGPTModelProbeError(status int, failure gptModelProbeUpstreamError) string {
	for _, code := range []string{failure.Code, failure.Type} {
		switch code {
		case "model_not_found", "model_not_available", "unsupported_model", "model_not_supported":
			return "model_not_available"
		case "insufficient_quota", "quota_exceeded", "quota_exhausted", "usage_limit_reached":
			return "model_quota_exhausted"
		case "rate_limit_exceeded", "rate_limited":
			return "model_rate_limited"
		case "permission_denied", "access_denied":
			return "model_access_denied"
		case "invalid_api_key", "invalid_token", "token_expired", "authentication_error":
			return "model_authentication_failed"
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return "model_authentication_failed"
	case http.StatusForbidden:
		return "model_access_denied"
	case http.StatusTooManyRequests:
		return "model_rate_limited"
	default:
		return "model_upstream_error"
	}
}

func gptModelProbeNetworkError(ctx context.Context, err error) string {
	var networkError net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return "model_timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "model_network_error"
}

// Only a completed response containing assistant text is a successful test.
// Final text replaces deltas because most Responses streams contain both.
func readGPTModelProbeStream(ctx context.Context, body io.Reader) (answer, actualModel, errorCode string) {
	limited := &io.LimitedReader{R: body, N: gptModelProbeMaxStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), gptModelProbeMaxStreamBytes+1)
	var data, deltas strings.Builder
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", actualModel, gptModelProbeNetworkError(ctx, ctx.Err())
		}
		if limited.N == 0 {
			return "", actualModel, "model_output_too_large"
		}
		line := scanner.Text()
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(payload, " "))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue
		}
		raw := data.String()
		data.Reset()
		if raw == "[DONE]" {
			return "", actualModel, "model_response_incomplete"
		}
		var event struct {
			Type     string                     `json:"type"`
			Delta    string                     `json:"delta"`
			Code     string                     `json:"code"`
			Error    gptModelProbeUpstreamError `json:"error"`
			Response *struct {
				Status string                      `json:"status"`
				Model  string                      `json:"model"`
				Error  *gptModelProbeUpstreamError `json:"error"`
				Output []struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if !utf8.ValidString(raw) || common.Unmarshal([]byte(raw), &event) != nil || event.Type == "" {
			return "", actualModel, "model_response_invalid"
		}
		if event.Response != nil && validGPTModelProbeModel(event.Response.Model) {
			actualModel = event.Response.Model
		}
		switch event.Type {
		case "response.output_text.delta":
			if deltas.Len()+len(event.Delta) > gptModelProbeMaxAnswerBytes {
				return "", actualModel, "model_output_too_large"
			}
			deltas.WriteString(event.Delta)
		case "response.failed":
			if event.Response != nil && event.Response.Error != nil {
				return "", actualModel, classifyGPTModelProbeError(0, *event.Response.Error)
			}
			return "", actualModel, "model_upstream_error"
		case "response.incomplete":
			return "", actualModel, "model_response_incomplete"
		case "error", "response.error":
			if event.Error.Code == "" {
				event.Error.Code = event.Code
			}
			return "", actualModel, classifyGPTModelProbeError(0, event.Error)
		case "response.completed":
			if event.Response == nil || event.Response.Status != "completed" || event.Response.Error != nil {
				return "", actualModel, "model_response_invalid"
			}
			var final strings.Builder
			for _, output := range event.Response.Output {
				if output.Type != "message" || output.Role != "assistant" {
					continue
				}
				for _, content := range output.Content {
					if content.Type != "output_text" {
						continue
					}
					if final.Len()+len(content.Text) > gptModelProbeMaxAnswerBytes {
						return "", actualModel, "model_output_too_large"
					}
					final.WriteString(content.Text)
				}
			}
			answer = final.String()
			if answer == "" && len(event.Response.Output) == 0 {
				answer = deltas.String()
			}
			if strings.TrimSpace(answer) == "" {
				return "", actualModel, "model_no_answer"
			}
			return answer, actualModel, ""
		}
	}
	if limited.N == 0 || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return "", actualModel, "model_output_too_large"
	}
	if scanner.Err() != nil {
		return "", actualModel, gptModelProbeNetworkError(ctx, scanner.Err())
	}
	return "", actualModel, "model_response_incomplete"
}
