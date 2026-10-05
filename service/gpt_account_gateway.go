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
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

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
	input.Name, input.Group, input.Models = strings.TrimSpace(input.Name), strings.TrimSpace(input.Group), strings.TrimSpace(input.Models)
	if input.Group == "" {
		input.Group = "default"
	}
	if input.Models == "" {
		input.Models = "gpt-5"
	}
	if input.Name == "" || len(input.Name) > 128 || len(input.Group) > 64 || strings.ContainsAny(input.Group, "\r\n") || len(input.Models) > 8192 || strings.ContainsAny(input.Models, "\r\n") || input.RemoteGroupID < 0 || len(input.ManagementKey) > 8192 || len(input.RelayKey) > 8192 || strings.ContainsAny(input.ManagementKey+input.RelayKey, "\r\n") {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider != "cpa" && input.Provider != "sub2api" {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider == "cpa" && input.ManagementVersion != "v8" && input.ManagementVersion != "legacy" {
		return nil, errors.New("invalid_gateway")
	}
	if input.Provider == "sub2api" && (input.ManagementVersion != "" || input.RemoteGroupID <= 0) {
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
	gateway       *model.GPTAccountGateway
	account       *model.GPTAccount
	client        *http.Client
	managementKey string
}

func newGPTGatewayClient(gateway *model.GPTAccountGateway, account *model.GPTAccount) (*gptGatewayClient, error) {
	if _, err := normalizeGPTGatewayURL(gateway.ManagementURL, gateway.AllowLocalHTTP); err != nil {
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
			if !gptGatewayAddressAllowed(address.IP, gateway.AllowLocalHTTP) {
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
	if !gptAccountOwnerActive(client.account) {
		return 0, errors.New("authorization_forbidden")
	}
	var current model.GPTAccount
	if model.DB.First(&current, client.account.ID).Error != nil || current.Generation != client.account.Generation || current.LeaseID != client.account.LeaseID || current.LeaseUntil <= time.Now().Unix() || current.GatewayID == nil || *current.GatewayID != client.gateway.ID {
		return 0, model.ErrGPTAccountStale
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
	// Do not lose the identity of an uncertain initial create. Resolve it before
	// starting a fresh OAuth import that could otherwise create another record.
	if secrets.GatewayImportAttempted && secrets.GatewayRemoteID == "" {
		return errors.New("gateway_import_uncertain")
	}
	secrets.GatewayOAuth, secrets.GatewayImportID, secrets.GatewayImportAttempted = &key, uuid.NewString(), false
	snapshot.AccountID = key.AccountID
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	return client.sync(ctx, secrets)
}

func SyncGPTAccountGateway(ctx context.Context, accountID int) (resultErr error) {
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
	if err := client.sync(ctx, secrets); err != nil {
		return err
	}
	if !refresh {
		return nil
	}
	var path string
	if gateway.Provider == "cpa" {
		path = client.cpaPath() + "/refresh"
	} else {
		path = "/api/v1/admin/openai/accounts/" + secrets.GatewayRemoteID + "/refresh"
	}
	var response struct {
		OK   bool `json:"ok"`
		Code *int `json:"code"`
	}
	status, err := client.request(ctx, http.MethodPost, path, map[string]string{"name": secrets.GatewayRemoteID}, &response)
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
	if secrets.GatewayOAuth == nil && secrets.GatewayRemoteID == "" {
		return errors.New("gateway_credentials_required")
	}
	var err error
	if client.gateway.Provider == "cpa" {
		err = client.syncCPA(ctx, secrets)
	} else {
		err = client.syncSub2API(ctx, secrets)
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
	secrets.GatewayRemoteID, secrets.GatewayImportAttempted = name, true
	if err := client.checkpoint(secrets, "pending", "", false); err != nil {
		return err
	}
	var response struct {
		Status string `json:"status"`
	}
	status, err = client.request(ctx, http.MethodPost, path+"?name="+url.QueryEscape(name), gptCPACredential{CodexOAuthKey: *secrets.GatewayOAuth, Binding: client.account.SecretID, ImportID: secrets.GatewayImportID}, &response)
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
	if err != nil || status == http.StatusNotFound || response.Code == nil || *response.Code != 0 || !client.ownedSubAccount(response.Data) || response.Data.Extra["new_api_import"] != secrets.GatewayImportID || (existing.ID != 0 && response.Data.ID != existing.ID) {
		return errors.New("gateway_import_uncertain")
	}
	secrets.GatewayRemoteID = strconv.FormatInt(response.Data.ID, 10)
	return nil
}
