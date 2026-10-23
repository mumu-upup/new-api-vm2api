package service

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthTokenURL = "https://auth.openai.com/oauth/token"
	codexJWTClaimPath  = "https://api.openai.com/auth"
	defaultHTTPTimeout = 20 * time.Second
)

type CodexOAuthTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func RefreshCodexOAuthToken(ctx context.Context, refreshToken string) (*CodexOAuthTokenResult, error) {
	return RefreshCodexOAuthTokenWithProxy(ctx, refreshToken, "")
}

func RefreshCodexOAuthTokenWithProxy(ctx context.Context, refreshToken string, proxyURL string) (*CodexOAuthTokenResult, error) {
	return RefreshGPTAccountOAuthTokenWithProxy(ctx, model.GPTAccountProtocolCodex, refreshToken, proxyURL)
}

func RefreshGPTAccountOAuthTokenWithProxy(ctx context.Context, protocol, refreshToken, proxyURL string) (*CodexOAuthTokenResult, error) {
	profile, err := oauthProfileForGPTAccountProtocol(protocol)
	if err != nil {
		return nil, err
	}
	client, err := getCodexOAuthHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	return refreshCodexOAuthToken(ctx, client, profile.TokenURL, profile.ClientID, refreshToken)
}

func refreshCodexOAuthToken(
	ctx context.Context,
	client *http.Client,
	tokenURL string,
	clientID string,
	refreshToken string,
) (*CodexOAuthTokenResult, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, errors.New("empty refresh_token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err := common.DecodeJson(io.LimitReader(resp.Body, 128<<10), &payload); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex oauth refresh failed: status=%d", resp.StatusCode)
	}

	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 || payload.ExpiresIn > 366*24*3600 {
		return nil, errors.New("codex oauth refresh response missing fields")
	}

	if strings.TrimSpace(payload.RefreshToken) == "" {
		payload.RefreshToken = rt
	}

	return &CodexOAuthTokenResult{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func getCodexOAuthHTTPClient(proxyURL string) (*http.Client, error) {
	baseClient, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	if common.TLSInsecureSkipVerify {
		var parsedProxy *url.URL
		if strings.TrimSpace(proxyURL) != "" {
			parsedProxy, _, err = common.ParseProxyURLRuntime(proxyURL)
			if err != nil {
				return nil, err
			}
		}
		baseClient, err = newHTTPClientFromPolicy(defaultHTTPTransportPolicy(), parsedProxy, &tls.Config{MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, err
		}
		// This credential-only transport is short lived and must not retain an
		// idle pool after the authorization operation finishes.
		baseClient.Transport.(*http.Transport).DisableKeepAlives = true
	}
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	clientCopy := *baseClient
	clientCopy.Timeout = defaultHTTPTimeout
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clientCopy, nil
}

func ExtractCodexAccountIDFromJWT(token string) (string, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return "", false
	}
	raw, ok := claims[codexJWTClaimPath]
	if !ok {
		return "", false
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := obj["chatgpt_account_id"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func ExtractEmailFromJWT(token string) (string, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return "", false
	}
	v, ok := claims["email"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func decodeJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := common.Unmarshal(payloadRaw, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// Excel identity claims are trusted only after issuer-key signature, API
// audience, expiry and client binding verification, never after JWT decoding.
type excelAccessIdentity struct {
	AccountID string
	Email     string
	Subject   string
}

func verifyExcelAccessToken(ctx context.Context, client *http.Client, provider *oidc.Provider, token, subject string) (*excelAccessIdentity, error) {
	var err error
	oauthCtx := oidc.ClientContext(ctx, client)
	if provider == nil {
		provider, err = oidc.NewProvider(oauthCtx, "https://auth.openai.com")
		if err != nil {
			return nil, errors.New("identity_verification_failed")
		}
	}
	verified, err := provider.Verifier(&oidc.Config{ClientID: "https://api.openai.com/v1", SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}}).Verify(oauthCtx, token)
	if err != nil || verified.Subject == "" || len(verified.Subject) > 255 || (subject != "" && verified.Subject != subject) {
		return nil, errors.New("identity_verification_failed")
	}
	var claims struct {
		ClientID        string `json:"client_id"`
		AuthorizedParty string `json:"azp"`
		NotBefore       int64  `json:"nbf"`
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		Auth            struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		Profile struct {
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
		} `json:"https://api.openai.com/profile"`
	}
	if verified.Claims(&claims) != nil || claims.NotBefore > time.Now().Unix() || (claims.ClientID == "" && claims.AuthorizedParty == "") || (claims.ClientID != "" && claims.ClientID != excelOAuthClientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != excelOAuthClientID) {
		return nil, errors.New("identity_verification_failed")
	}
	if claims.Email == "" {
		claims.Email, claims.EmailVerified = claims.Profile.Email, claims.Profile.EmailVerified
	}
	if !claims.EmailVerified || strings.TrimSpace(claims.Email) == "" || strings.TrimSpace(claims.Auth.AccountID) == "" {
		return nil, errors.New("identity_mismatch")
	}
	return &excelAccessIdentity{AccountID: claims.Auth.AccountID, Email: strings.TrimSpace(claims.Email), Subject: verified.Subject}, nil
}
