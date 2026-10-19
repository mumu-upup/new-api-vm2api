package service

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GPTGatewayChannelInput is the minimal public channel configuration accepted
// by the direct gateway JSON importer. It intentionally contains only the
// relay credentials needed by a channel; management credentials and account
// OAuth documents are rejected instead of being sent as relay keys.
type GPTGatewayChannelInput struct {
	Name                  string
	Provider              string
	BaseURL               string
	APIKey                string
	Group                 string
	Models                string
	ForcedReasoningEffort string
}

// ParseGPTGatewayChannelJSON normalizes the supported CPA, Sub2API, VM2API, and Prism
// channel JSON shapes into one channel input. This is a local parser only: it
// never contacts a gateway and never returns the supplied credential in an
// error.
func ParseGPTGatewayChannelJSON(raw string) (GPTGatewayChannelInput, error) {
	if len(strings.TrimSpace(raw)) == 0 || len(raw) > 128<<10 {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_json_invalid")
	}
	var document map[string]any
	if common.UnmarshalJsonStr(raw, &document) != nil || document == nil {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_json_invalid")
	}
	readString := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := document[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	provider := strings.ToLower(readString("provider", "provider_type", "platform", "type"))
	switch provider {
	case "cpa", "cliproxyapi", "cli-proxy-api", "cli_proxy_api":
		provider = "cpa"
	case "sub2api", "sub2-api", "sub2_api":
		provider = "sub2api"
	case "vm2api", "vm2-api", "vm2_api":
		provider = "vm2api"
	case "prism", "prism-bridge", "prism_bridge":
		provider = "prism"
	default:
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_provider_required")
	}
	baseURL := readString("relay_url", "relayUrl", "base_url", "baseUrl", "endpoint", "url")
	if baseURL == "" {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_url_required")
	}
	allowLocalHTTP := false
	if value, ok := document["allow_local_http"]; ok {
		var valid bool
		allowLocalHTTP, valid = value.(bool)
		if !valid {
			return GPTGatewayChannelInput{}, errors.New("gateway_channel_url_invalid")
		}
	}
	var err error
	baseURL, err = normalizeGPTGatewayURL(baseURL, allowLocalHTTP)
	if err != nil {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_url_invalid")
	}
	// Both relay adaptors append the versioned request path. Accept the
	// client-facing /v1 URL without producing /v1/v1 when forwarding.
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	apiKey := readString("api_key", "apiKey", "relay_key", "relayKey", "key", "token")
	if apiKey == "" {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_key_required")
	}
	if strings.ContainsAny(apiKey, "\r\n") || len(apiKey) > 8192 {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_key_invalid")
	}
	models := "gpt-5"
	if provider == "prism" {
		models = "gpt-6.1-sol"
	}
	if value := readString("model"); value != "" {
		models = value
	}
	if value, ok := document["models"]; ok {
		switch typed := value.(type) {
		case string:
			models = strings.TrimSpace(typed)
		case []any:
			items := make([]string, 0, len(typed))
			for _, item := range typed {
				modelName, ok := item.(string)
				if !ok || strings.TrimSpace(modelName) == "" {
					return GPTGatewayChannelInput{}, errors.New("gateway_channel_models_invalid")
				}
				items = append(items, strings.TrimSpace(modelName))
			}
			if len(items) == 0 {
				return GPTGatewayChannelInput{}, errors.New("gateway_channel_models_invalid")
			}
			models = strings.Join(items, ",")
		default:
			return GPTGatewayChannelInput{}, errors.New("gateway_channel_models_invalid")
		}
	}
	modelNames := make([]string, 0)
	for modelName := range strings.SplitSeq(models, ",") {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" || len(modelName) > 255 {
			return GPTGatewayChannelInput{}, errors.New("gateway_channel_models_invalid")
		}
		modelNames = append(modelNames, modelName)
	}
	models = strings.Join(modelNames, ",")
	forcedReasoningEffort := ""
	if provider == "prism" {
		forcedReasoningEffort = "max"
		if value, exists := document["forced_reasoning_effort"]; exists {
			value, ok := value.(string)
			if !ok {
				return GPTGatewayChannelInput{}, errors.New("gateway_channel_json_invalid")
			}
			forcedReasoningEffort = strings.ToLower(strings.TrimSpace(value))
			if forcedReasoningEffort == "" {
				forcedReasoningEffort = "max"
			}
		}
	}
	if err := (&relaydto.ChannelOtherSettings{ForcedReasoningEffort: forcedReasoningEffort}).ValidateForcedReasoningEffort(); err != nil {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_json_invalid")
	}
	group := readString("group", "group_name", "groupName")
	name := readString("name", "channel_name", "channelName")
	if name == "" {
		name = strings.ToUpper(provider) + " channel"
	}
	if len(name) > 128 || len(group) > 64 || len(models) > 8192 || strings.ContainsAny(name+group+models, "\r\n") {
		return GPTGatewayChannelInput{}, errors.New("gateway_channel_json_invalid")
	}
	return GPTGatewayChannelInput{Name: name, Provider: provider, BaseURL: baseURL, APIKey: apiKey, Group: group, Models: models, ForcedReasoningEffort: forcedReasoningEffort}, nil
}

// CreateGPTGatewayChannel creates the relay channel in the same transaction
// as its ability rows, so a malformed ability update cannot leave a half-made
// channel behind.
func CreateGPTGatewayChannel(input GPTGatewayChannelInput) (*model.Channel, error) {
	forcedReasoningEffort := strings.ToLower(strings.TrimSpace(input.ForcedReasoningEffort))
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if forcedReasoningEffort == "" && (provider == "prism" || provider == "prism-bridge" || provider == "prism_bridge") {
		forcedReasoningEffort = "max"
	}
	if err := (&relaydto.ChannelOtherSettings{ForcedReasoningEffort: forcedReasoningEffort}).ValidateForcedReasoningEffort(); err != nil {
		return nil, err
	}
	group, err := model.ResolveGPTAccountGroup(input.Group)
	if err != nil {
		return nil, err
	}
	channelType := constant.ChannelTypeOpenAI
	if input.Provider == "sub2api" {
		channelType = constant.ChannelTypeSub2API
	}
	channel := &model.Channel{Type: channelType, Name: input.Name, Key: input.APIKey, BaseURL: &input.BaseURL, Group: group, Models: input.Models, Status: common.ChannelStatusEnabled, CreatedTime: common.GetTimestamp()}
	if forcedReasoningEffort != "" {
		channel.SetOtherSettings(relaydto.ChannelOtherSettings{ForcedReasoningEffort: forcedReasoningEffort})
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(channel).Error; err != nil {
			return err
		}
		return channel.AddAbilities(tx)
	})
	if err != nil {
		return nil, err
	}
	model.InitChannelCache()
	return channel, nil
}

type GPTAccountGatewayInput struct {
	Name              string `json:"name"`
	Provider          string `json:"provider"`
	ManagementVersion string `json:"management_version"`
	ManagementURL     string `json:"management_url"`
	RelayURL          string `json:"relay_url"`
	AllowLocalHTTP    bool   `json:"allow_local_http"`
	RemoteGroupID     int64  `json:"remote_group_id"`
	Group             string `json:"group"`
	Models            string `json:"models"`
	ManagementKey     string `json:"management_key"`
	RelayKey          string `json:"relay_key"`
}

type GPTAccountGatewaySecrets struct {
	ManagementKey string `json:"management_key"`
	RelayKey      string `json:"relay_key"`
}

func DecryptGPTAccountGatewaySecrets(gateway *model.GPTAccountGateway) (*GPTAccountGatewaySecrets, error) {
	raw, err := decryptGPTSecret(gateway.SecretsCiphertext, "gpt-gateway:"+gateway.SecretID)
	if err != nil {
		return nil, err
	}
	var secrets GPTAccountGatewaySecrets
	if common.UnmarshalJsonStr(raw, &secrets) != nil || secrets.ManagementKey == "" || secrets.RelayKey == "" {
		return nil, ErrGPTAccountEncryption
	}
	return &secrets, nil
}

func normalizeGPTGatewayURL(value string, allowLocal bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\r\n") || len(value) > 2048 {
		return "", errors.New("invalid_gateway")
	}
	for segment := range strings.SplitSeq(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("invalid_gateway")
		}
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && allowLocal && local) {
		return "", errors.New("invalid_gateway")
	}
	if local && !allowLocal {
		return "", errors.New("invalid_gateway")
	}
	if ip != nil && !gptGatewayAddressAllowed(ip, allowLocal) {
		return "", errors.New("invalid_gateway")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func gptGatewayAddressAllowed(ip net.IP, allowLocal bool) bool {
	if ip.IsLoopback() {
		return allowLocal
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.Equal(net.ParseIP("169.254.169.254"))
}

func SaveGPTAccountGateway(existing *model.GPTAccountGateway, input GPTAccountGatewayInput) (*model.GPTAccountGateway, error) {
	if existing != nil && existing.ManagedAccountID != nil {
		return nil, errors.New("gateway_immutable")
	}
	input.Name, input.Group, input.Models = strings.TrimSpace(input.Name), strings.TrimSpace(input.Group), strings.TrimSpace(input.Models)
	var groupErr error
	input.Group, groupErr = model.ResolveGPTAccountGroup(input.Group)
	if groupErr != nil {
		return nil, groupErr
	}
	if input.Models == "" {
		input.Models = "gpt-5"
	}
	if input.Name == "" || len(input.Name) > 128 || len(input.Group) > 64 || strings.ContainsAny(input.Group, "\r\n") || len(input.Models) > 8192 || strings.ContainsAny(input.Models, "\r\n") || input.RemoteGroupID < 0 || len(input.ManagementKey) > 8192 || len(input.RelayKey) > 8192 || strings.ContainsAny(input.ManagementKey+input.RelayKey, "\r\n") {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider != "cpa" && input.Provider != "sub2api" && input.Provider != "vm2api" {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider == "cpa" && input.ManagementVersion != "v8" && input.ManagementVersion != "legacy" {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider == "sub2api" && (input.ManagementVersion != "" || input.RemoteGroupID <= 0) {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider == "vm2api" && (input.ManagementVersion != "" || input.RemoteGroupID != 0 || input.ManagementURL != input.RelayURL) {
		return nil, errors.New("invalid_gateway")
	}
	var err error
	input.ManagementURL, err = normalizeGPTGatewayURL(input.ManagementURL, input.AllowLocalHTTP)
	if err != nil {
		return nil, err
	}
	input.RelayURL, err = normalizeGPTGatewayURL(input.RelayURL, input.AllowLocalHTTP)
	if err != nil {
		return nil, err
	}
	gateway := &model.GPTAccountGateway{SecretID: uuid.NewString()}
	secrets := &GPTAccountGatewaySecrets{}
	if existing != nil {
		copied := *existing
		gateway = &copied
		if gateway.Provider != input.Provider || gateway.ManagementURL != input.ManagementURL || gateway.RelayURL != input.RelayURL || gateway.ManagementVersion != input.ManagementVersion || gateway.AllowLocalHTTP != input.AllowLocalHTTP || gateway.RemoteGroupID != input.RemoteGroupID {
			return nil, errors.New("gateway_immutable")
		}
		secrets, err = DecryptGPTAccountGatewaySecrets(gateway)
		if err != nil {
			return nil, err
		}
	}
	if input.ManagementKey != "" {
		secrets.ManagementKey = input.ManagementKey
	}
	if input.RelayKey != "" {
		secrets.RelayKey = input.RelayKey
	}
	if strings.TrimSpace(secrets.ManagementKey) == "" || strings.TrimSpace(secrets.RelayKey) == "" {
		return nil, errors.New("gateway_keys_required")
	}
	encoded, err := common.Marshal(secrets)
	if err != nil {
		return nil, ErrGPTAccountEncryption
	}
	gateway.SecretsCiphertext, err = encryptGPTSecret(string(encoded), "gpt-gateway:"+gateway.SecretID)
	if err != nil {
		return nil, err
	}
	gateway.Name, gateway.Provider, gateway.ManagementVersion = input.Name, input.Provider, input.ManagementVersion
	gateway.ManagementURL, gateway.RelayURL, gateway.AllowLocalHTTP, gateway.RemoteGroupID = input.ManagementURL, input.RelayURL, input.AllowLocalHTTP, input.RemoteGroupID
	gateway.Group, gateway.Models, gateway.HasManagementKey, gateway.HasRelayKey = input.Group, input.Models, true, true
	if err := model.SaveGPTAccountGateway(gateway, secrets.RelayKey); err != nil {
		return nil, err
	}
	model.InitChannelCache()
	return gateway, nil
}

// ApplyGPTAccountGateway keeps direct and gateway associations mutually exclusive
// and immutable once the account has been created. A nil update means unchanged.
func ApplyGPTAccountGateway(account *model.GPTAccount, gatewayID *int) error {
	if account.ID == 0 && gatewayID == nil {
		resolved, err := model.ResolveGPTAccountDefaultGateway()
		if err != nil {
			return err
		}
		gatewayID = resolved
	}
	if account.ID == 0 && GPTAccountVM2APIIsolationEnabled() && (gatewayID == nil || *gatewayID == 0) {
		return errors.New("gateway_required")
	}
	if gatewayID == nil {
		gatewayID = account.GatewayID
		if gatewayID == nil {
			return nil
		}
	}
	if *gatewayID < 0 {
		return errors.New("invalid_gateway")
	}
	current := 0
	if account.GatewayID != nil {
		current = *account.GatewayID
	}
	if account.ID != 0 && current != *gatewayID {
		return errors.New("gateway_immutable")
	}
	if *gatewayID == 0 {
		return nil
	}
	if account.ChannelID != nil {
		return errors.New("gateway_immutable")
	}
	var gateway model.GPTAccountGateway
	if model.DB.First(&gateway, *gatewayID).Error != nil || gateway.ChannelID == nil {
		return errors.New("gateway_not_found")
	}
	if gateway.ManagedAccountID != nil && (account.ID == 0 || *gateway.ManagedAccountID != account.ID) {
		return errors.New("gateway_immutable")
	}
	account.GatewayID = gatewayID
	account.Group, account.Models = gateway.Group, gateway.Models
	if account.GatewaySyncStatus == "" {
		account.GatewaySyncStatus = "pending"
	}
	return nil
}

func PopulateGPTAccountGateways(accounts []model.GPTAccount) error {
	ids := make([]int, 0, len(accounts))
	for _, account := range accounts {
		if account.GatewayID != nil {
			ids = append(ids, *account.GatewayID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var gateways []model.GPTAccountGateway
	if err := model.DB.Where("id IN ?", ids).Find(&gateways).Error; err != nil {
		return err
	}
	byID := make(map[int]model.GPTAccountGateway, len(gateways))
	channelIDs := make([]int, 0, len(gateways))
	for _, gateway := range gateways {
		byID[gateway.ID] = gateway
		if gateway.ChannelID != nil {
			channelIDs = append(channelIDs, *gateway.ChannelID)
		}
	}
	names, err := model.GetChannelNamesByIds(channelIDs)
	if err != nil {
		return err
	}
	for i := range accounts {
		if accounts[i].GatewayID == nil {
			continue
		}
		gateway, ok := byID[*accounts[i].GatewayID]
		if !ok {
			continue
		}
		accounts[i].GatewayName, accounts[i].GatewayProvider, accounts[i].GatewayChannelID = gateway.Name, gateway.Provider, gateway.ChannelID
		accounts[i].Group, accounts[i].Models = gateway.Group, gateway.Models
		if gateway.ChannelID != nil {
			accounts[i].GatewayChannelName = names[*gateway.ChannelID]
		}
	}
	return nil
}

type gptGatewayClient struct {
	gateway        *model.GPTAccountGateway
	account        *model.GPTAccount
	client         *http.Client
	managementKey  string
	readAccount    *model.GPTAccount
	previousStatus string
}

func newGPTGatewayClient(gateway *model.GPTAccountGateway, account *model.GPTAccount) (*gptGatewayClient, error) {
	managed := managedVM2APIGatewayAllowed(gateway)
	if gateway.ManagedAccountID != nil && !managed {
		return nil, errors.New("invalid_gateway")
	}
	if _, err := normalizeGPTGatewayURL(gateway.ManagementURL, gateway.AllowLocalHTTP); err != nil && !managed {
		return nil, err
	}
	secrets, err := DecryptGPTAccountGatewaySecrets(gateway)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("gateway_unavailable")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("gateway_unavailable")
		}
		for _, address := range addresses {
			if !gptGatewayAddressAllowed(address.IP, gateway.AllowLocalHTTP) && !(managed && address.IP.Equal(net.ParseIP(host))) {
				return nil, errors.New("gateway_unavailable")
			}
		}
		for _, address := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("gateway_unavailable")
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &gptGatewayClient{gateway: gateway, account: account, managementKey: secrets.ManagementKey, client: client}, nil
}

// request never returns remote error text, response bodies, URLs, or credentials.
func (client *gptGatewayClient) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	if client.account != nil && !gptAccountOwnerActive(client.account) {
		return 0, errors.New("authorization_forbidden")
	}
	if client.account != nil {
		var current model.GPTAccount
		if model.DB.First(&current, client.account.ID).Error != nil || current.OwnerID != client.account.OwnerID || current.Generation != client.account.Generation || current.LeaseID != client.account.LeaseID || current.LeaseUntil <= time.Now().Unix() || current.GatewayID == nil || *current.GatewayID != client.gateway.ID {
			return 0, model.ErrGPTAccountStale
		}
	}
	if client.readAccount != nil {
		var current model.GPTAccount
		if !gptAccountOwnerActive(client.readAccount) || model.DB.First(&current, client.readAccount.ID).Error != nil || current.OwnerID != client.readAccount.OwnerID || current.Generation != client.readAccount.Generation || current.GatewayID == nil || *current.GatewayID != client.gateway.ID {
			return 0, model.ErrGPTAccountStale
		}
	}
	var gateway model.GPTAccountGateway
	if model.DB.First(&gateway, client.gateway.ID).Error != nil || gateway.Generation != client.gateway.Generation {
		return 0, model.ErrGPTAccountStale
	}
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = common.Marshal(body)
		if err != nil {
			return 0, errors.New("gateway_import_failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, client.gateway.ManagementURL+path, strings.NewReader(string(encoded)))
	if err != nil {
		return 0, errors.New("gateway_unavailable")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client.gateway.Provider == "cpa" {
		req.Header.Set("Authorization", "Bearer "+client.managementKey)
	} else {
		req.Header.Set("x-api-key", client.managementKey)
	}
	response, err := client.client.Do(req)
	if err != nil {
		return 0, errors.New("gateway_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, errors.New("gateway_import_failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return response.StatusCode, errors.New("gateway_import_failed")
	}
	if out != nil && common.Unmarshal(raw, out) != nil {
		return response.StatusCode, errors.New("gateway_import_failed")
	}
	return response.StatusCode, nil
}

func (client *gptGatewayClient) checkpoint(secrets *GPTAccountSecrets, status, code string, complete bool) error {
	if !gptAccountOwnerActive(client.account) {
		return errors.New("authorization_forbidden")
	}
	if err := EncryptGPTAccountSecrets(client.account, *secrets); err != nil {
		return err
	}
	return model.StoreGPTAccountGatewayCheckpoint(client.account, client.gateway, client.account.SecretsCiphertext, status, code, complete)
}

func gptGatewayImportRejected(status int) bool {
	// These responses reject the request before applying it. Timeouts, conflicts,
	// malformed successes and server errors remain uncertain to prevent duplicate
	// accounts or replaying credentials after a remote write may have succeeded.
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// StoreVerifiedGPTAccountGatewayCredential accepts only the result of the host's
// verified OAuth flow. It persists retryable import intent before contacting the
// configured gateway, never treating a remote acknowledgement as OAuth proof.
func StoreVerifiedGPTAccountGatewayCredential(ctx context.Context, snapshot *model.GPTAccount, key CodexOAuthKey) error {
	if snapshot.GatewayID == nil || snapshot.ChannelID != nil {
		return errors.New("invalid_gateway")
	}
	expires, err := time.Parse(time.RFC3339, key.Expired)
	if err != nil || !expires.After(time.Now()) || key.AccessToken == "" || key.RefreshToken == "" || key.IDToken == "" || key.AccountID == "" || !strings.EqualFold(key.Email, snapshot.Email) || (snapshot.AccountID != "" && snapshot.AccountID != key.AccountID) || snapshot.OAuthSubject == "" {
		return errors.New("identity_mismatch")
	}
	var gateway model.GPTAccountGateway
	if model.DB.First(&gateway, *snapshot.GatewayID).Error != nil {
		return errors.New("gateway_not_found")
	}
	client, err := newGPTGatewayClient(&gateway, snapshot)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	secrets, err := DecryptGPTAccountSecrets(snapshot)
	if err != nil {
		return err
	}
	// Reconcile any previous remote write before accepting a fresh OAuth bundle.
	// VM2API has no import id to compare, so a slot without matching credentials
	// remains uncertain and the new refresh token must not be replayed.
	if secrets.GatewayImportAttempted {
		if err := client.sync(ctx, secrets); err != nil {
			return err
		}
	}
	secrets.GatewayOAuth, secrets.GatewayImportID, secrets.GatewayImportAttempted = &key, uuid.NewString(), false
	snapshot.AccountID = key.AccountID
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	if gateway.Provider == "vm2api" && gateway.ManagedAccountID == nil && GPTAccountVM2APIIsolationEnabled() && secrets.GatewayRemoteID == "" {
		return model.RequestGPTAccountVM2APIInstance(snapshot, false)
	}
	return client.sync(ctx, secrets)
}

func SyncGPTAccountGateway(ctx context.Context, accountID int) (resultErr error) {
	var instance model.GPTAccountVM2APIInstance
	if model.DB.Where("account_id = ?", accountID).Limit(1).Find(&instance).Error != nil {
		return errors.New("operation_failed")
	}
	if instance.AccountID != 0 && instance.Status != "ready" {
		return EnsureGPTAccountVM2APIInstance(ctx, accountID)
	}
	snapshot, err := AcquireGPTAccountJob(accountID, false)
	if err != nil {
		return err
	}
	defer func() { ReleaseGPTAccountJob(snapshot, GPTAccountErrorCode(resultErr)) }()
	return syncGPTAccountGatewaySnapshot(ctx, snapshot, false)
}

func RefreshManagedGPTAccountGateway(ctx context.Context, snapshot *model.GPTAccount) error {
	return syncGPTAccountGatewaySnapshot(ctx, snapshot, true)
}

func syncGPTAccountGatewaySnapshot(ctx context.Context, snapshot *model.GPTAccount, refresh bool) error {
	if snapshot.GatewayID == nil || snapshot.ChannelID != nil {
		return errors.New("invalid_gateway")
	}
	if !gptAccountOwnerActive(snapshot) {
		return errors.New("authorization_forbidden")
	}
	var gateway model.GPTAccountGateway
	if model.DB.First(&gateway, *snapshot.GatewayID).Error != nil {
		return errors.New("gateway_not_found")
	}
	client, err := newGPTGatewayClient(&gateway, snapshot)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	secrets, err := DecryptGPTAccountSecrets(snapshot)
	if err != nil {
		return err
	}
	if gateway.Provider == "vm2api" && gateway.ManagedAccountID == nil && GPTAccountVM2APIIsolationEnabled() && secrets.GatewayRemoteID == "" && !secrets.GatewayImportAttempted {
		if err := model.RequestGPTAccountVM2APIInstance(snapshot, false); err != nil {
			return err
		}
		return errors.New("instance_not_ready")
	}
	if err := client.sync(ctx, secrets); err != nil {
		return err
	}
	if !refresh {
		return nil
	}
	var path string
	if gateway.Provider == "cpa" {
		path = client.cpaPath() + "/refresh"
	} else if gateway.Provider == "sub2api" {
		path = "/api/v1/admin/openai/accounts/" + secrets.GatewayRemoteID + "/refresh"
	} else {
		path = "/api/panel/vms/" + url.PathEscape(secrets.GatewayRemoteID) + "/oauth/refresh"
	}
	var response struct {
		OK   bool `json:"ok"`
		Code *int `json:"code"`
	}
	var vmResponse struct {
		OK   bool `json:"ok"`
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	body := map[string]string{"name": secrets.GatewayRemoteID}
	if gateway.Provider == "vm2api" {
		body = map[string]string{"vm_id": secrets.GatewayRemoteID}
	}
	if gateway.Provider == "vm2api" {
		status, err := client.request(ctx, http.MethodPost, path, body, &vmResponse)
		if err != nil || status == http.StatusNotFound || !vmResponse.OK || !vmResponse.Data.OK {
			return errors.New("gateway_import_failed")
		}
		return nil
	}
	status, err := client.request(ctx, http.MethodPost, path, body, &response)
	if err != nil || status == http.StatusNotFound || (gateway.Provider == "cpa" && !response.OK) || (gateway.Provider == "sub2api" && (response.Code == nil || *response.Code != 0)) {
		return errors.New("gateway_import_failed")
	}
	return nil
}

func (client *gptGatewayClient) sync(ctx context.Context, secrets *GPTAccountSecrets) (resultErr error) {
	defer func() {
		if resultErr != nil {
			code := GPTAccountErrorCode(resultErr)
			_ = client.checkpoint(secrets, "error", code, false)
		}
	}()
	var instance model.GPTAccountVM2APIInstance
	if model.DB.Where("account_id = ?", client.account.ID).Limit(1).Find(&instance).Error != nil {
		return errors.New("operation_failed")
	}
	if instance.AccountID != 0 && (instance.GatewayID == nil || *instance.GatewayID != client.gateway.ID || instance.Status == "pending_cleanup") {
		return errors.New("instance_not_ready")
	}
	if secrets.GatewayOAuth == nil && secrets.GatewayRemoteID == "" {
		return errors.New("gateway_credentials_required")
	}
	var err error
	if client.gateway.Provider == "cpa" {
		err = client.syncCPA(ctx, secrets)
	} else if client.gateway.Provider == "sub2api" {
		err = client.syncSub2API(ctx, secrets)
	} else {
		err = client.syncVM2API(ctx, secrets)
	}
	if err != nil {
		return err
	}
	// The remote gateway now owns refreshes. Retain no obsolete local refresh
	// token that a future sync could accidentally replay over rotated credentials.
	secrets.GatewayOAuth = nil
	secrets.GatewayImportAttempted = false
	return client.checkpoint(secrets, "synced", "", true)
}

func (client *gptGatewayClient) cpaPath() string {
	if client.gateway.ManagementVersion == "legacy" {
		return "/v0/management/auth-files"
	}
	return "/v8/management/credentials"
}

// GPTAccountVM2APIStatus is the redacted VM2API panel payload exposed by the
// host dashboard. The nested maps intentionally retain quota fields added by
// newer VM2API versions without persisting or returning credentials.
type GPTAccountVM2APIStatus struct {
	AccountID int            `json:"account_id"`
	VMID      string         `json:"vm_id"`
	GatewayID int            `json:"gateway_id"`
	VM        map[string]any `json:"vm"`
	Quota     map[string]any `json:"quota,omitempty"`
}

func vm2apiRemoteID(account *model.GPTAccount, secrets *GPTAccountSecrets) string {
	if secrets.GatewayRemoteID != "" {
		return secrets.GatewayRemoteID
	}
	id := "new-api-" + model.GPTChannelKeyHash(account.SecretID)[:32]
	if account.GatewayID != nil {
		var gateway model.GPTAccountGateway
		if model.DB.First(&gateway, *account.GatewayID).Error == nil && gateway.ManagedAccountID != nil && *gateway.ManagedAccountID == account.ID {
			id += "-isolated"
		}
	}
	return id
}

func loadGPTAccountVM2APIClient(accountID int, mutation bool) (*model.GPTAccount, *model.GPTAccountGateway, *GPTAccountSecrets, *gptGatewayClient, error) {
	var account model.GPTAccount
	if model.DB.First(&account, accountID).Error != nil || account.GatewayID == nil {
		return nil, nil, nil, nil, errors.New("account_not_found")
	}
	var gateway model.GPTAccountGateway
	if model.DB.First(&gateway, *account.GatewayID).Error != nil || gateway.Provider != "vm2api" {
		return nil, nil, nil, nil, errors.New("gateway_not_found")
	}
	secrets, err := DecryptGPTAccountSecrets(&account)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	client, err := newGPTGatewayClient(&gateway, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	client.readAccount = &account
	if mutation {
		snapshot, err := AcquireGPTAccountJob(accountID, false)
		if err != nil {
			client.client.CloseIdleConnections()
			return nil, nil, nil, nil, err
		}
		client.previousStatus = account.Status
		client.account = snapshot
		client.readAccount = nil
		var instance model.GPTAccountVM2APIInstance
		result := model.DB.Where("account_id = ?", accountID).Limit(1).Find(&instance)
		if result.Error != nil || (instance.AccountID != 0 && instance.Status != "ready") {
			client.releaseMutation(nil)
			client.client.CloseIdleConnections()
			return nil, nil, nil, nil, errors.New("instance_not_ready")
		}
		account = *snapshot
	}
	return &account, &gateway, secrets, client, nil
}

func (client *gptGatewayClient) releaseMutation(err error) {
	if err == nil {
		model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_id = ?", client.account.ID, client.account.Generation, client.account.LeaseID).Update("status", client.previousStatus)
	}
	ReleaseGPTAccountJob(client.account, GPTAccountErrorCode(err))
}

func GetGPTAccountVM2APIStatus(ctx context.Context, accountID int) (*GPTAccountVM2APIStatus, error) {
	account, gateway, secrets, client, err := loadGPTAccountVM2APIClient(accountID, false)
	if err != nil {
		return nil, err
	}
	defer client.client.CloseIdleConnections()
	vmID := vm2apiRemoteID(account, secrets)
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			VM map[string]any `json:"vm"`
		} `json:"data"`
	}
	status, err := client.request(ctx, http.MethodGet, "/api/panel/vms/"+url.PathEscape(vmID), nil, &response)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || !response.OK {
		return nil, errors.New("gateway_remote_missing")
	}
	if response.Data.VM["id"] != vmID {
		return nil, errors.New("gateway_remote_conflict")
	}
	vm := vm2apiPublicFields(response.Data.VM, "id", "name", "status", "platform", "family", "email", "schedulable", "schedule_disabled_reason", "max_concurrency", "max_rpm", "max_sessions", "plan_type", "kernel", "runtime_type", "container", "has_token", "has_refresh", "expires_at", "refreshed_at", "created_at")
	quotaSource := response.Data.VM
	if usage, ok := response.Data.VM["codex_usage"].(map[string]any); ok {
		if quota, ok := usage["quota"].(map[string]any); ok {
			quotaSource = quota
		}
	}
	if quota, ok := response.Data.VM["quota"].(map[string]any); ok {
		quotaSource = quota
	}
	quota := vm2apiPublicFields(quotaSource, "utilization_5h", "utilization_7d", "reset_5h", "reset_7d", "status_5h", "status_7d", "updated_at")
	result := &GPTAccountVM2APIStatus{AccountID: account.ID, VMID: vmID, GatewayID: gateway.ID, VM: vm, Quota: quota}
	return result, nil
}

func UpdateGPTAccountVM2APIPolicy(ctx context.Context, accountID int, body map[string]any) (resultErr error) {
	for key, value := range body {
		switch key {
		case "max_concurrency", "max_rpm", "max_sessions":
			number, ok := value.(float64)
			if !ok || number < 0 || number > 1000 || number != float64(int(number)) {
				return errors.New("invalid_gateway")
			}
		default:
			return errors.New("invalid_gateway")
		}
	}
	if len(body) == 0 {
		return errors.New("invalid_gateway")
	}
	account, _, secrets, client, err := loadGPTAccountVM2APIClient(accountID, true)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	defer func() { client.releaseMutation(resultErr) }()
	status, err := client.request(ctx, http.MethodPatch, "/api/panel/vms/"+url.PathEscape(vm2apiRemoteID(account, secrets)), body, nil)
	if err != nil || status < 200 || status >= 300 {
		return errors.New("gateway_import_failed")
	}
	return nil
}

func SetGPTAccountVM2APISchedulable(ctx context.Context, accountID int, schedulable bool) (resultErr error) {
	account, _, secrets, client, err := loadGPTAccountVM2APIClient(accountID, true)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	defer func() { client.releaseMutation(resultErr) }()
	status, err := client.request(ctx, http.MethodPost, "/api/panel/vms/"+url.PathEscape(vm2apiRemoteID(account, secrets))+"/schedulable", map[string]any{"schedulable": schedulable}, nil)
	if err != nil || status < 200 || status >= 300 {
		return errors.New("gateway_import_failed")
	}
	return nil
}

func RefreshGPTAccountVM2APIQuota(ctx context.Context, accountID int) (resultErr error) {
	account, _, secrets, client, err := loadGPTAccountVM2APIClient(accountID, true)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	defer func() { client.releaseMutation(resultErr) }()
	status, err := client.request(ctx, http.MethodPost, "/api/panel/vms/"+url.PathEscape(vm2apiRemoteID(account, secrets))+"/openai-quota/refresh", nil, nil)
	if err != nil || status < 200 || status >= 300 {
		return errors.New("gateway_import_failed")
	}
	return nil
}

func ListGPTAccountVM2APILogs(ctx context.Context, accountID int, limit int) (map[string]any, error) {
	account, _, secrets, client, err := loadGPTAccountVM2APIClient(accountID, false)
	if err != nil {
		return nil, err
	}
	defer client.client.CloseIdleConnections()
	if limit < 1 || limit > 100 {
		limit = 50
	}
	params := url.Values{"vm_id": {vm2apiRemoteID(account, secrets)}, "limit": {strconv.Itoa(limit)}}
	var response map[string]any
	status, err := client.request(ctx, http.MethodGet, "/api/panel/request-logs?"+params.Encode(), nil, &response)
	if err != nil || status < 200 || status >= 300 {
		return nil, errors.New("gateway_import_failed")
	}
	items := make([]map[string]any, 0)
	if rows, ok := response["items"].([]any); ok {
		for _, row := range rows {
			item, ok := row.(map[string]any)
			if !ok || item["vm_id"] != vm2apiRemoteID(account, secrets) {
				continue
			}
			items = append(items, vm2apiPublicFields(item, "id", "request_id", "vm_id", "created_at", "model", "requested_model", "upstream_model", "status", "protocol", "stream", "first_token_ms", "duration_ms", "input_tokens", "output_tokens", "error_code", "error_class", "attempt_count", "final_state", "stop_reason"))
		}
	}
	return map[string]any{"items": items, "total": len(items), "limit": limit}, nil
}

// Public monitoring fields never contain nested provider documents or freeform
// errors; those can embed OAuth credentials, proxy passwords or request bodies.
func vm2apiPublicFields(source map[string]any, keys ...string) map[string]any {
	result := make(map[string]any)
	for _, key := range keys {
		switch value := source[key].(type) {
		case nil, bool, float64:
			result[key] = value
		case string:
			if len(value) <= 512 {
				result[key] = value
			}
		}
	}
	return result
}

type gptCPACredential struct {
	CodexOAuthKey
	Binding  string `json:"new_api_account"`
	ImportID string `json:"new_api_import"`
}

func (client *gptGatewayClient) syncCPA(ctx context.Context, secrets *GPTAccountSecrets) error {
	name := "new-api-" + model.GPTChannelKeyHash(client.account.SecretID)[:32] + ".json"
	if secrets.GatewayRemoteID != "" && secrets.GatewayRemoteID != name {
		return errors.New("gateway_remote_conflict")
	}
	path := client.cpaPath()
	var existing gptCPACredential
	status, err := client.request(ctx, http.MethodGet, path+"/download?name="+url.QueryEscape(name), nil, &existing)
	if err != nil {
		return err
	}
	if status != http.StatusNotFound {
		if existing.Binding != client.account.SecretID || existing.Type != "codex" || existing.AccountID != client.account.AccountID || !strings.EqualFold(existing.Email, client.account.Email) {
			return errors.New("gateway_remote_conflict")
		}
		if secrets.GatewayOAuth == nil || existing.ImportID == secrets.GatewayImportID {
			secrets.GatewayRemoteID = name
			return nil
		}
	} else if secrets.GatewayImportAttempted {
		return errors.New("gateway_import_uncertain")
	} else if secrets.GatewayRemoteID != "" {
		return errors.New("gateway_remote_missing")
	}
	if secrets.GatewayOAuth == nil {
		return errors.New("gateway_credentials_required")
	}
	expires, expiryErr := time.Parse(time.RFC3339, secrets.GatewayOAuth.Expired)
	if expiryErr != nil || !expires.After(time.Now()) {
		return errors.New("gateway_credentials_required")
	}
	previousRemoteID := secrets.GatewayRemoteID
	secrets.GatewayRemoteID, secrets.GatewayImportAttempted = name, true
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	var response struct {
		Status string `json:"status"`
	}
	status, err = client.request(ctx, http.MethodPost, path+"?name="+url.QueryEscape(name), gptCPACredential{CodexOAuthKey: *secrets.GatewayOAuth, Binding: client.account.SecretID, ImportID: secrets.GatewayImportID}, &response)
	if gptGatewayImportRejected(status) {
		secrets.GatewayRemoteID, secrets.GatewayImportAttempted = previousRemoteID, false
		return errors.New("gateway_import_failed")
	}
	if err != nil || status == http.StatusNotFound || response.Status != "ok" {
		return errors.New("gateway_import_uncertain")
	}
	existing = gptCPACredential{}
	status, err = client.request(ctx, http.MethodGet, path+"/download?name="+url.QueryEscape(name), nil, &existing)
	if err != nil || status == http.StatusNotFound || existing.Binding != client.account.SecretID || existing.ImportID != secrets.GatewayImportID || existing.AccountID != client.account.AccountID || !strings.EqualFold(existing.Email, client.account.Email) {
		return errors.New("gateway_import_uncertain")
	}
	return nil
}

type gptSubAccount struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Extra       map[string]any `json:"extra"`
	Credentials struct {
		Email     string `json:"email"`
		AccountID string `json:"chatgpt_account_id"`
	} `json:"credentials"`
}

type gptSubAccountResponse struct {
	Code *int          `json:"code"`
	Data gptSubAccount `json:"data"`
}

func (client *gptGatewayClient) ownedSubAccount(account gptSubAccount) bool {
	return account.ID > 0 && account.Platform == "openai" && account.Type == "oauth" && account.Extra["new_api_account"] == client.account.SecretID && account.Credentials.AccountID == client.account.AccountID && strings.EqualFold(account.Credentials.Email, client.account.Email)
}

func (client *gptGatewayClient) syncSub2API(ctx context.Context, secrets *GPTAccountSecrets) error {
	name := "new-api-" + model.GPTChannelKeyHash(client.account.SecretID)[:32]
	var existing gptSubAccount
	if secrets.GatewayRemoteID != "" {
		remoteID, err := strconv.ParseInt(secrets.GatewayRemoteID, 10, 64)
		if err != nil || remoteID <= 0 {
			return errors.New("gateway_remote_conflict")
		}
		var response gptSubAccountResponse
		status, err := client.request(ctx, http.MethodGet, "/api/v1/admin/accounts/"+secrets.GatewayRemoteID, nil, &response)
		if err != nil {
			return err
		}
		if status == http.StatusNotFound {
			return errors.New("gateway_remote_missing")
		}
		if response.Code == nil || *response.Code != 0 || !client.ownedSubAccount(response.Data) || response.Data.ID != remoteID {
			return errors.New("gateway_remote_conflict")
		}
		existing = response.Data
	} else {
		var response struct {
			Code *int `json:"code"`
			Data struct {
				Items []gptSubAccount `json:"items"`
				Total int             `json:"total"`
			} `json:"data"`
		}
		status, err := client.request(ctx, http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=100&search="+url.QueryEscape(name), nil, &response)
		if err != nil {
			return err
		}
		if status == http.StatusNotFound || response.Code == nil || *response.Code != 0 || response.Data.Total > 100 {
			return errors.New("gateway_import_failed")
		}
		for _, account := range response.Data.Items {
			if account.Name != name {
				continue
			}
			if !client.ownedSubAccount(account) || existing.ID != 0 {
				return errors.New("gateway_remote_conflict")
			}
			existing = account
		}
		if existing.ID == 0 && secrets.GatewayImportAttempted {
			return errors.New("gateway_import_uncertain")
		}
	}
	if existing.ID != 0 {
		secrets.GatewayRemoteID = strconv.FormatInt(existing.ID, 10)
		if secrets.GatewayOAuth == nil || existing.Extra["new_api_import"] == secrets.GatewayImportID {
			return nil
		}
	}
	if secrets.GatewayOAuth == nil {
		return errors.New("gateway_credentials_required")
	}
	expires, expiryErr := time.Parse(time.RFC3339, secrets.GatewayOAuth.Expired)
	if expiryErr != nil || !expires.After(time.Now()) {
		return errors.New("gateway_credentials_required")
	}
	key := secrets.GatewayOAuth
	credentials := map[string]any{"access_token": key.AccessToken, "refresh_token": key.RefreshToken, "id_token": key.IDToken, "email": key.Email, "chatgpt_account_id": key.AccountID, "expires_at": key.Expired, "client_id": codexOAuthClientID}
	extra := map[string]any{"new_api_account": client.account.SecretID, "new_api_import": secrets.GatewayImportID}
	body := map[string]any{"type": "oauth", "credentials": credentials, "extra": extra}
	path := "/api/v1/admin/accounts"
	if existing.ID != 0 {
		path += "/" + secrets.GatewayRemoteID + "/apply-oauth-credentials"
	} else {
		body["name"], body["platform"], body["group_ids"], body["concurrency"], body["priority"] = name, "openai", []int64{client.gateway.RemoteGroupID}, 3, 50
	}
	secrets.GatewayImportAttempted = true
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	var response gptSubAccountResponse
	status, err := client.request(ctx, http.MethodPost, path, body, &response)
	if gptGatewayImportRejected(status) {
		secrets.GatewayImportAttempted = false
		return errors.New("gateway_import_failed")
	}
	if err != nil || status == http.StatusNotFound || response.Code == nil || *response.Code != 0 || !client.ownedSubAccount(response.Data) || response.Data.Extra["new_api_import"] != secrets.GatewayImportID || (existing.ID != 0 && response.Data.ID != existing.ID) {
		return errors.New("gateway_import_uncertain")
	}
	secrets.GatewayRemoteID = strconv.FormatInt(response.Data.ID, 10)
	return nil
}

type gptVM2APIVM struct {
	Status           string `json:"status"`
	Schedulable      bool   `json:"schedulable"`
	ID               string `json:"id"`
	Platform         string `json:"platform"`
	Family           string `json:"family"`
	HasToken         bool   `json:"has_token"`
	HasRefresh       bool   `json:"has_refresh"`
	Email            string `json:"email"`
	OrgUUID          string `json:"org_uuid"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
}

type gptVM2APIVMResponse struct {
	OK   bool `json:"ok"`
	Data struct {
		VM gptVM2APIVM `json:"vm"`
	} `json:"data"`
}

func ownedVM2APISlot(vm gptVM2APIVM, vmID string) bool {
	return vm.ID == vmID && vm.Platform == "openai" && vm.Family == "codex"
}

func (client *gptGatewayClient) syncVM2API(ctx context.Context, secrets *GPTAccountSecrets) error {
	vmID := vm2apiRemoteID(client.account, &GPTAccountSecrets{})
	if secrets.GatewayRemoteID != "" && secrets.GatewayRemoteID != vmID {
		return errors.New("gateway_remote_conflict")
	}
	var existing gptVM2APIVMResponse
	status, err := client.request(ctx, http.MethodGet, "/api/panel/vms/"+url.PathEscape(vmID), nil, &existing)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		if secrets.GatewayImportAttempted {
			return errors.New("gateway_import_uncertain")
		}
		if secrets.GatewayOAuth == nil {
			return errors.New("gateway_credentials_required")
		}
		// Slot provisioning carries no OAuth credentials. A slow image pull or
		// startup must not consume the one-time credential import attempt.
		secrets.GatewayRemoteID = vmID
		if err := client.checkpoint(secrets, "pending", "", false); err != nil {
			return err
		}
		createBody := map[string]any{
			"id": vmID, "name": vmID, "platform": "openai", "family": "codex",
			"start": true, "auto_allocate_proxy": true,
		}
		if client.gateway.ManagedAccountID != nil {
			createBody["machine"] = map[string]any{"memory": "512m", "docker_cpus": 1}
			createBody["kernel"] = "archlinux"
		}
		createStatus, createErr := client.request(ctx, http.MethodPost, "/api/panel/vms/create", createBody, &existing)
		if gptGatewayImportRejected(createStatus) {
			secrets.GatewayRemoteID = ""
			return errors.New("gateway_import_failed")
		}
		if createErr != nil || !existing.OK || !ownedVM2APISlot(existing.Data.VM, vmID) {
			// VM2API persists the slot before waiting for its runtime to boot.
			// Read it back after an ambiguous response; never blindly recreate it
			// or assume that an existing slot is empty.
			existing = gptVM2APIVMResponse{}
			status, err = client.request(ctx, http.MethodGet, "/api/panel/vms/"+url.PathEscape(vmID), nil, &existing)
			if err != nil || status == http.StatusNotFound {
				return errors.New("gateway_unavailable")
			}
		}
	}
	if !existing.OK || !ownedVM2APISlot(existing.Data.VM, vmID) {
		return errors.New("gateway_remote_conflict")
	}
	if client.gateway.ManagedAccountID != nil && existing.Data.VM.Status != "running" {
		if (existing.Data.VM.HasToken || existing.Data.VM.HasRefresh) && !ownedVM2APIIdentity(existing.Data.VM, vmID, client.account) {
			return errors.New("gateway_remote_conflict")
		}
		existing, err = client.startVM2APISlot(ctx, vmID)
		if err != nil {
			return err
		}
	}
	secrets.GatewayRemoteID = vmID
	if existing.Data.VM.HasToken || existing.Data.VM.HasRefresh {
		if client.gateway.ManagedAccountID != nil && (!ownedVM2APIIdentity(existing.Data.VM, vmID, client.account) || existing.Data.VM.Status != "running") {
			return errors.New("gateway_remote_conflict")
		}
		remoteAccountID := existing.Data.VM.ChatGPTAccountID
		if remoteAccountID == "" {
			remoteAccountID = existing.Data.VM.OrgUUID
		}
		if existing.Data.VM.Email == "" && remoteAccountID == "" {
			return errors.New("gateway_remote_conflict")
		}
		if existing.Data.VM.Email != "" && !strings.EqualFold(existing.Data.VM.Email, client.account.Email) {
			return errors.New("gateway_remote_conflict")
		}
		if remoteAccountID != "" && remoteAccountID != client.account.AccountID {
			return errors.New("gateway_remote_conflict")
		}
		secrets.GatewayOAuth = nil
		secrets.GatewayImportAttempted = false
		return nil
	}
	if secrets.GatewayImportAttempted {
		return errors.New("gateway_import_uncertain")
	}
	if secrets.GatewayOAuth == nil {
		return errors.New("gateway_credentials_required")
	}
	expires, expiryErr := time.Parse(time.RFC3339, secrets.GatewayOAuth.Expired)
	if expiryErr != nil || !expires.After(time.Now()) {
		return errors.New("gateway_credentials_required")
	}
	key := secrets.GatewayOAuth
	account := map[string]any{
		"access_token":       key.AccessToken,
		"refresh_token":      key.RefreshToken,
		"id_token":           key.IDToken,
		"email":              key.Email,
		"chatgpt_account_id": key.AccountID,
		"expires_at":         expires.UnixMilli(),
	}
	encoded, err := common.Marshal(account)
	if err != nil {
		return errors.New("gateway_import_failed")
	}
	// Only the credential-bearing request needs conservative replay protection.
	secrets.GatewayImportAttempted = true
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	var importResponse gptVM2APIVMResponse
	importStatus, importErr := client.request(ctx, http.MethodPost, "/api/panel/vms/import", map[string]any{"vm_id": vmID, "auth_json": string(encoded)}, &importResponse)
	if gptGatewayImportRejected(importStatus) {
		secrets.GatewayImportAttempted = false
		return errors.New("gateway_import_failed")
	}
	if importErr != nil || importStatus < 200 || importStatus >= 300 || !importResponse.OK || !ownedVM2APISlot(importResponse.Data.VM, vmID) {
		return errors.New("gateway_import_uncertain")
	}
	if client.gateway.ManagedAccountID != nil {
		var verified gptVM2APIVMResponse
		status, err := client.request(ctx, http.MethodGet, "/api/panel/vms/"+url.PathEscape(vmID), nil, &verified)
		if err != nil || status != 200 || !verified.OK || !ownedVM2APIIdentity(verified.Data.VM, vmID, client.account) || verified.Data.VM.Status != "running" {
			return errors.New("gateway_import_uncertain")
		}
	}
	return nil
}
