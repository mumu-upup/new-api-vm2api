package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GPTAccountVM2APIIsolationEnabled() bool {
	return strings.TrimSpace(os.Getenv("VM2API_INSTANCE_MANAGER_SOCKET")) != ""
}

type gptVM2APIManagerResult struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ContainerName string `json:"container_name"`
	DataDirectory string `json:"data_directory"`
	Image         string `json:"image"`
	Port          int    `json:"port"`
	ManagementURL string `json:"management_url"`
	RelayURL      string `json:"relay_url"`
	ManagementKey string `json:"management_key"`
	RelayKey      string `json:"relay_key"`
	ErrorCode     string `json:"error_code"`
}

func vm2apiManagerRequest(ctx context.Context, method, id, action string) (*gptVM2APIManagerResult, error) {
	socket := strings.TrimSpace(os.Getenv("VM2API_INSTANCE_MANAGER_SOCKET"))
	if !filepath.IsAbs(socket) || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, errors.New("instance_unavailable")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}, ResponseHeaderTimeout: 90 * time.Second, MaxResponseHeaderBytes: 8 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := "http://manager/v1/instances/" + id
	if action != "" {
		path += "/" + action
	}
	req, err := http.NewRequestWithContext(ctx, method, path, strings.NewReader("{}"))
	if err != nil {
		return nil, errors.New("instance_unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("instance_unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("instance_unavailable")
	}
	var result gptVM2APIManagerResult
	if common.Unmarshal(raw, &result) != nil {
		return nil, errors.New("instance_unavailable")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if result.ErrorCode == "capacity_exhausted" {
			return nil, errors.New("instance_capacity_exhausted")
		}
		return nil, errors.New("instance_unavailable")
	}
	if method == http.MethodDelete {
		if result.ID != id || result.Status != "deleted" {
			return nil, errors.New("instance_invalid_response")
		}
		return &result, nil
	}
	if result.ID != id || result.ContainerName != "vm2api-account-"+id || result.Port < 1024 || result.Port > 65535 {
		return nil, errors.New("instance_invalid_response")
	}
	host := net.ParseIP(strings.TrimSpace(os.Getenv("VM2API_INSTANCE_HOST")))
	if host == nil || (!host.IsPrivate() && !host.IsLoopback()) {
		return nil, errors.New("instance_invalid_response")
	}
	expected := "http://" + net.JoinHostPort(host.String(), strconv.Itoa(result.Port))
	if result.ManagementURL != expected || result.RelayURL != expected || len(result.DataDirectory) > 2048 || !filepath.IsAbs(result.DataDirectory) || len(result.Image) > 512 {
		return nil, errors.New("instance_invalid_response")
	}
	switch result.Status {
	case "running", "stopped", "missing", "created", "exited", "restarting":
	default:
		return nil, errors.New("instance_invalid_response")
	}
	if method == http.MethodPut && (result.ManagementKey == "" || result.RelayKey == "" || len(result.ManagementKey) > 8192 || len(result.RelayKey) > 8192 || strings.ContainsAny(result.ManagementKey+result.RelayKey, "\r\n")) {
		return nil, errors.New("instance_invalid_response")
	}
	return &result, nil
}

// Only a persisted one-account binding may use the fixed private manager host.
// The general gateway URL and DNS policy remains unchanged.
func managedVM2APIGatewayAllowed(gateway *model.GPTAccountGateway) bool {
	if gateway.ManagedAccountID == nil || gateway.Provider != "vm2api" || gateway.ID <= 0 {
		return false
	}
	var instance model.GPTAccountVM2APIInstance
	if model.DB.Where("account_id = ? AND gateway_id = ?", *gateway.ManagedAccountID, gateway.ID).First(&instance).Error != nil || instance.Port < 1024 || instance.Port > 65535 {
		return false
	}
	var persisted model.GPTAccountGateway
	if model.DB.First(&persisted, gateway.ID).Error != nil || persisted.ManagedAccountID == nil || *persisted.ManagedAccountID != *gateway.ManagedAccountID || persisted.ManagementURL != gateway.ManagementURL || persisted.RelayURL != gateway.RelayURL {
		return false
	}
	host := net.ParseIP(strings.TrimSpace(os.Getenv("VM2API_INSTANCE_HOST")))
	if host == nil || (!host.IsPrivate() && !host.IsLoopback()) {
		return false
	}
	expected := "http://" + net.JoinHostPort(host.String(), strconv.Itoa(instance.Port))
	return gateway.ManagementURL == expected && gateway.RelayURL == expected
}

type GPTAccountVM2APIInstanceStatus struct {
	AccountID       int    `json:"account_id"`
	Isolation       string `json:"isolation"`
	InstanceID      string `json:"instance_id,omitempty"`
	GatewayID       *int   `json:"gateway_id"`
	SourceGatewayID int    `json:"source_gateway_id,omitempty"`
	Status          string `json:"status"`
	RuntimeStatus   string `json:"runtime_status,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	ContainerName   string `json:"container_name,omitempty"`
	DataDirectory   string `json:"data_directory,omitempty"`
	Image           string `json:"image,omitempty"`
	Port            int    `json:"port,omitempty"`
	UpdatedAt       int64  `json:"updated_at"`
	VMID            string `json:"vm_id,omitempty"`
}

func GetGPTAccountVM2APIInstance(ctx context.Context, accountID int) (*GPTAccountVM2APIInstanceStatus, error) {
	var account model.GPTAccount
	if model.DB.First(&account, accountID).Error != nil || !gptAccountOwnerActive(&account) {
		return nil, errors.New("account_not_found")
	}
	var instance model.GPTAccountVM2APIInstance
	result := model.DB.Where("account_id = ?", accountID).Limit(1).Find(&instance)
	if result.Error != nil {
		return nil, errors.New("operation_failed")
	}
	if result.RowsAffected == 0 {
		return &GPTAccountVM2APIInstanceStatus{AccountID: accountID, Isolation: "shared", GatewayID: account.GatewayID, Status: "shared", UpdatedAt: account.UpdatedAt}, nil
	}
	isolation := "shared"
	if instance.GatewayID != nil {
		isolation = "dedicated"
	}
	status := &GPTAccountVM2APIInstanceStatus{AccountID: accountID, Isolation: isolation, InstanceID: instance.InstanceID, GatewayID: instance.GatewayID, SourceGatewayID: instance.SourceGatewayID, Status: instance.Status, ErrorCode: instance.ErrorCode, ContainerName: instance.ContainerName, DataDirectory: instance.DataDirectory, Image: instance.Image, Port: instance.Port, UpdatedAt: instance.UpdatedAt, VMID: "new-api-" + instance.InstanceID + "-isolated"}
	// Persisted state is always returned, even when the runtime is offline.
	runtimeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if runtime, err := vm2apiManagerRequest(runtimeCtx, http.MethodGet, instance.InstanceID, ""); err == nil {
		status.RuntimeStatus = runtime.Status
	} else {
		status.RuntimeStatus = "unavailable"
	}
	return status, nil
}

// RequestGPTAccountVM2APIInstance records an explicit migration request. The
// worker uses the ordinary account lease, shared with OAuth and refresh jobs.
func RequestGPTAccountVM2APIInstance(ctx context.Context, accountID int) (resultErr error) {
	if !GPTAccountVM2APIIsolationEnabled() {
		return errors.New("instance_unavailable")
	}
	snapshot, err := AcquireGPTAccountJob(accountID, false)
	if err != nil {
		return err
	}
	defer func() { ReleaseGPTAccountJob(snapshot, GPTAccountErrorCode(resultErr)) }()
	secrets, err := DecryptGPTAccountSecrets(snapshot)
	if err != nil {
		return err
	}
	migration := secrets.GatewayRemoteID != "" || secrets.GatewayOAuth == nil
	if err := model.RequestGPTAccountVM2APIInstance(snapshot, migration); err != nil {
		return err
	}
	var instance model.GPTAccountVM2APIInstance
	if err := model.DB.First(&instance, "account_id = ?", accountID).Error; err != nil {
		return err
	}
	status := "pending"
	if instance.Status == "ready" {
		status = "ready"
	}
	return model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_id = ?", accountID, snapshot.Generation, snapshot.LeaseID).Update("status", status).Error
}

func StartGPTAccountVM2APIInstanceJob(accountID int) {
	var instance model.GPTAccountVM2APIInstance
	if model.DB.Where("account_id = ? AND status <> ?", accountID, "ready").Limit(1).Find(&instance).Error != nil || instance.AccountID == 0 || instance.Status == "pending_cleanup" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
		defer cancel()
		_ = EnsureGPTAccountVM2APIInstance(ctx, accountID)
	}()
}

func ScheduleGPTAccountVM2APIInstances() {
	if !GPTAccountVM2APIIsolationEnabled() {
		return
	}
	var instances []model.GPTAccountVM2APIInstance
	if model.DB.Where("status IN ?", []string{"pending", "provisioning", "syncing", "failed"}).Order("updated_at ASC").Limit(10).Find(&instances).Error != nil {
		return
	}
	for _, instance := range instances {
		var account model.GPTAccount
		if model.DB.First(&account, instance.AccountID).Error == nil && account.LeaseUntil <= time.Now().Unix() && account.NextRetryAt <= time.Now().Unix() && account.FailureCount < 3 {
			StartGPTAccountVM2APIInstanceJob(account.ID)
		}
	}
}

func EnsureGPTAccountVM2APIInstance(ctx context.Context, accountID int) (resultErr error) {
	snapshot, err := AcquireGPTAccountJob(accountID, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(snapshot.LeaseUntil, 0).Add(-15*time.Second))
	defer cancel()
	defer func() { ReleaseGPTAccountJob(snapshot, GPTAccountErrorCode(resultErr)); model.InitChannelCache() }()
	var instance model.GPTAccountVM2APIInstance
	if model.DB.First(&instance, "account_id = ?", accountID).Error != nil {
		return errors.New("instance_not_found")
	}
	if !gptAccountOwnerActive(snapshot) {
		return errors.New("authorization_forbidden")
	}
	if instance.Status == "pending_cleanup" {
		return errors.New("account_busy")
	}
	if instance.Status == "stopped" {
		return errors.New("instance_not_running")
	}
	defer func() {
		if resultErr != nil {
			_ = model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"status": "failed", "error_code": GPTAccountErrorCode(resultErr)})
		}
	}()
	if instance.Status == "ready" {
		return model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_id = ?", snapshot.ID, snapshot.Generation, snapshot.LeaseID).Update("status", "ready").Error
	}
	if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"status": "provisioning", "error_code": ""}); err != nil {
		return err
	}
	runtime, err := vm2apiManagerRequest(ctx, http.MethodPut, instance.InstanceID, "")
	if err != nil {
		return err
	}
	if runtime.Status != "running" {
		return errors.New("instance_not_running")
	}
	instance.ContainerName, instance.DataDirectory, instance.Image, instance.Port = runtime.ContainerName, runtime.DataDirectory, runtime.Image, runtime.Port
	if instance.Migration && instance.GatewayID == nil {
		if err := prepareVM2APIMigration(ctx, snapshot, &instance); err != nil {
			return err
		}
	}
	if instance.GatewayID == nil {
		var template model.GPTAccountGateway
		if model.DB.First(&template, instance.SourceGatewayID).Error != nil || template.ManagedAccountID != nil {
			return errors.New("gateway_not_found")
		}
		gateway := &model.GPTAccountGateway{Name: snapshot.Name + " VM2API", Provider: "vm2api", ManagementURL: runtime.ManagementURL, RelayURL: runtime.RelayURL, Group: template.Group, Models: template.Models, SecretID: uuid.NewString(), HasManagementKey: true, HasRelayKey: true}
		raw, err := common.Marshal(GPTAccountGatewaySecrets{ManagementKey: runtime.ManagementKey, RelayKey: runtime.RelayKey})
		if err != nil {
			return err
		}
		gateway.SecretsCiphertext, err = encryptGPTSecret(string(raw), "gpt-gateway:"+gateway.SecretID)
		if err != nil {
			return err
		}
		secrets, err := DecryptGPTAccountSecrets(snapshot)
		if err != nil {
			return err
		}
		secrets.GatewayRemoteID = ""
		secrets.GatewayImportAttempted = false
		if err := EncryptGPTAccountSecrets(snapshot, *secrets); err != nil {
			return err
		}
		if err := model.BindGPTAccountVM2APIInstance(snapshot, &instance, gateway, runtime.RelayKey, snapshot.SecretsCiphertext); err != nil {
			return err
		}
	}
	if instance.Migration && instance.PackageCiphertext != "" {
		return importVM2APIMigration(ctx, snapshot, &instance)
	}
	return syncGPTAccountGatewaySnapshot(ctx, snapshot, false)
}

func prepareVM2APIMigration(ctx context.Context, snapshot *model.GPTAccount, instance *model.GPTAccountVM2APIInstance) error {
	if instance.PackageCiphertext != "" {
		return nil
	}
	var source model.GPTAccountGateway
	if model.DB.First(&source, instance.SourceGatewayID).Error != nil || snapshot.GatewayID == nil || *snapshot.GatewayID != source.ID || source.ManagedAccountID != nil {
		return errors.New("gateway_immutable")
	}
	secrets, err := DecryptGPTAccountSecrets(snapshot)
	if err != nil {
		return err
	}
	client, err := newGPTGatewayClient(&source, snapshot)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	slotID := vm2apiRemoteID(snapshot, secrets)
	path := "/api/panel/vms/" + url.PathEscape(slotID)
	var remote gptVM2APIVMResponse
	status, err := client.request(ctx, http.MethodGet, path, nil, &remote)
	if err != nil || status == 404 || !remote.OK || !ownedVM2APIIdentity(remote.Data.VM, slotID, snapshot) {
		return errors.New("gateway_remote_conflict")
	}
	if instance.SourceStatus == "" {
		if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"source_status": remote.Data.VM.Status, "source_schedulable": remote.Data.VM.Schedulable}); err != nil {
			return err
		}
		instance.SourceStatus, instance.SourceSchedulable = remote.Data.VM.Status, remote.Data.VM.Schedulable
	}
	// Stop the old runtime before exporting rotating credentials. An ambiguous
	// stop is not permission to transfer; a subsequent GET must confirm it.
	if _, err := client.request(ctx, http.MethodPost, path+"/stop", nil, nil); err != nil {
		return err
	}
	remote = gptVM2APIVMResponse{}
	status, err = client.request(ctx, http.MethodGet, path, nil, &remote)
	if err != nil || status != 200 || !remote.OK || remote.Data.VM.Status != "stopped" || remote.Data.VM.Schedulable {
		return errors.New("instance_source_not_paused")
	}
	if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"source_paused": true}); err != nil {
		return err
	}
	var exported struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	status, err = client.request(ctx, http.MethodGet, path+"/package", nil, &exported)
	if err != nil || status != 200 || !exported.OK {
		return errors.New("instance_migration_failed")
	}
	vm, ok := exported.Data["vm"].(map[string]any)
	if !ok || vm["id"] != slotID || vm["platform"] != "openai" || vm["family"] != "codex" {
		return errors.New("gateway_remote_conflict")
	}
	credential, ok := exported.Data["credential"].(map[string]any)
	if !ok {
		return errors.New("gateway_credentials_required")
	}
	accounts, ok := credential["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		return errors.New("gateway_remote_conflict")
	}
	account, ok := accounts[0].(map[string]any)
	email, _ := account["email"].(string)
	accountID, _ := account["chatgpt_account_id"].(string)
	if !ok || !strings.EqualFold(email, snapshot.Email) || accountID != snapshot.AccountID {
		return errors.New("gateway_remote_conflict")
	}
	vm["id"] = "new-api-" + instance.InstanceID + "-isolated"
	encoded, err := common.Marshal(exported.Data)
	if err != nil || len(encoded) > 256<<10 {
		return errors.New("instance_migration_failed")
	}
	ciphertext, err := encryptGPTSecret(string(encoded), "gpt-instance:"+instance.InstanceID)
	if err != nil {
		return err
	}
	if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"package_ciphertext": ciphertext}); err != nil {
		return err
	}
	instance.PackageCiphertext = ciphertext
	return nil
}

func importVM2APIMigration(ctx context.Context, snapshot *model.GPTAccount, instance *model.GPTAccountVM2APIInstance) error {
	var gateway model.GPTAccountGateway
	if instance.GatewayID == nil || model.DB.First(&gateway, *instance.GatewayID).Error != nil {
		return errors.New("gateway_not_found")
	}
	client, err := newGPTGatewayClient(&gateway, snapshot)
	if err != nil {
		return err
	}
	defer client.client.CloseIdleConnections()
	slotID := "new-api-" + instance.InstanceID + "-isolated"
	var remote gptVM2APIVMResponse
	status, err := client.request(ctx, http.MethodGet, "/api/panel/vms/"+slotID, nil, &remote)
	if err != nil {
		return err
	}
	if status == 404 {
		if instance.ImportAttempted {
			return errors.New("gateway_import_uncertain")
		}
		raw, err := decryptGPTSecret(instance.PackageCiphertext, "gpt-instance:"+instance.InstanceID)
		if err != nil {
			return err
		}
		var payload map[string]any
		if common.UnmarshalJsonStr(raw, &payload) != nil {
			return errors.New("instance_migration_failed")
		}
		if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"import_attempted": true, "status": "syncing"}); err != nil {
			return err
		}
		status, err = client.request(ctx, http.MethodPost, "/api/panel/vms/package", payload, &remote)
		if gptGatewayImportRejected(status) {
			_ = model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"import_attempted": false})
			return errors.New("instance_migration_failed")
		}
		// Always read back: a successful import response alone is not readiness.
		remote = gptVM2APIVMResponse{}
		status, err = client.request(ctx, http.MethodGet, "/api/panel/vms/"+slotID, nil, &remote)
	}
	if err != nil || status != 200 || !remote.OK || !ownedVM2APIIdentity(remote.Data.VM, slotID, snapshot) {
		return errors.New("gateway_import_uncertain")
	}
	if remote.Data.VM.Status != "running" {
		remote, err = client.startVM2APISlot(ctx, slotID)
		if err != nil {
			return err
		}
		if !ownedVM2APIIdentity(remote.Data.VM, slotID, snapshot) {
			return errors.New("gateway_remote_conflict")
		}
	}
	// The exported package is paused because its source was stopped. Explicitly
	// enable the destination only after identity and runtime verification.
	if _, err := client.request(ctx, http.MethodPost, "/api/panel/vms/"+slotID+"/schedulable", map[string]any{"schedulable": instance.SourceSchedulable}, nil); err != nil {
		return err
	}
	secrets, err := DecryptGPTAccountSecrets(snapshot)
	if err != nil {
		return err
	}
	secrets.GatewayRemoteID = slotID
	secrets.GatewayOAuth = nil
	secrets.GatewayImportAttempted = false
	return client.checkpoint(secrets, "synced", "", true)
}

// startVM2APISlot recovers a persisted slot after boot failure without sending
// its OAuth package again. A timed-out start is reconciled with an exact GET.
func (client *gptGatewayClient) startVM2APISlot(ctx context.Context, slotID string) (gptVM2APIVMResponse, error) {
	_, _ = client.request(ctx, http.MethodPost, "/api/panel/vms/"+url.PathEscape(slotID)+"/start", nil, nil)
	var response gptVM2APIVMResponse
	status, err := client.request(ctx, http.MethodGet, "/api/panel/vms/"+url.PathEscape(slotID), nil, &response)
	if err != nil || status != 200 || !response.OK || !ownedVM2APISlot(response.Data.VM, slotID) || response.Data.VM.Status != "running" {
		return response, errors.New("instance_not_running")
	}
	return response, nil
}

func ownedVM2APIIdentity(vm gptVM2APIVM, id string, account *model.GPTAccount) bool {
	remoteID := vm.ChatGPTAccountID
	if remoteID == "" {
		remoteID = vm.OrgUUID
	}
	return ownedVM2APISlot(vm, id) && (vm.HasToken || vm.HasRefresh) && strings.EqualFold(vm.Email, account.Email) && remoteID == account.AccountID
}

func ManageGPTAccountVM2APIInstance(ctx context.Context, accountID int, action string) (resultErr error) {
	switch action {
	case "start", "stop", "restart", "rebuild":
	default:
		return errors.New("invalid_gateway")
	}
	snapshot, err := AcquireGPTAccountJob(accountID, false)
	if err != nil {
		return err
	}
	defer func() { ReleaseGPTAccountJob(snapshot, GPTAccountErrorCode(resultErr)) }()
	if !gptAccountOwnerActive(snapshot) {
		return errors.New("authorization_forbidden")
	}
	var instance model.GPTAccountVM2APIInstance
	if model.DB.First(&instance, "account_id = ?", accountID).Error != nil || instance.GatewayID == nil || (instance.Status != "ready" && instance.Status != "failed" && instance.Status != "stopped") {
		return errors.New("instance_not_ready")
	}
	if err := setManagedVM2APIChannelStatus(snapshot, &instance, common.ChannelStatusAutoDisabled); err != nil {
		return err
	}
	nextStatus := "syncing"
	if action == "stop" {
		nextStatus = "stopped"
	}
	if err := model.StoreGPTAccountVM2APIProgress(snapshot, map[string]any{"status": nextStatus, "error_code": ""}); err != nil {
		return err
	}
	defer func() {
		values := map[string]any{"error_code": GPTAccountErrorCode(resultErr)}
		if resultErr != nil && action != "stop" {
			values["status"] = "failed"
		}
		_ = model.StoreGPTAccountVM2APIProgress(snapshot, values)
	}()
	if _, err := vm2apiManagerRequest(ctx, http.MethodPost, instance.InstanceID, action); err != nil {
		return err
	}
	if action == "stop" {
		return model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_id = ?", accountID, snapshot.Generation, snapshot.LeaseID).Update("status", "ready").Error
	}
	// Runtime start acknowledges Docker acceptance, not HTTP readiness. Any
	// failure remains durably retryable, and explicit Start can recover a stopped
	// container after a partially failed restart/rebuild.
	if instance.Migration && instance.PackageCiphertext != "" {
		resultErr = importVM2APIMigration(ctx, snapshot, &instance)
	} else {
		resultErr = syncGPTAccountGatewaySnapshot(ctx, snapshot, false)
	}
	model.InitChannelCache()
	return resultErr
}

func setManagedVM2APIChannelStatus(snapshot *model.GPTAccount, instance *model.GPTAccountVM2APIInstance, status int) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.LockGPTAccountInstanceJob(tx, snapshot); err != nil {
			return err
		}
		if instance.GatewayID == nil {
			return nil
		}
		var gateway model.GPTAccountGateway
		if tx.First(&gateway, *instance.GatewayID).Error != nil || gateway.ManagedAccountID == nil || *gateway.ManagedAccountID != snapshot.ID || gateway.ChannelID == nil {
			return model.ErrGPTAccountStale
		}
		var channel model.Channel
		if err := tx.First(&channel, *gateway.ChannelID).Error; err != nil {
			return err
		}
		channel.Status = status
		if err := tx.Model(&channel).Update("status", status).Error; err != nil {
			return err
		}
		return channel.UpdateAbilities(tx)
	})
	model.InitChannelCache()
	return err
}

func DeleteGPTAccountWithInstance(ctx context.Context, accountID int) error {
	now := time.Now().Unix()
	lease := uuid.NewString()
	result := model.DB.Model(&model.GPTAccount{}).Where("id = ? AND lease_until <= ?", accountID, now).Updates(map[string]any{"lease_id": lease, "lease_until": now + gptAccountJobLeaseSeconds, "generation": gorm.Expr("generation + 1"), "status": "pending_cleanup"})
	if result.Error != nil {
		return errors.New("operation_failed")
	}
	if result.RowsAffected != 1 {
		return errors.New("account_busy")
	}
	var snapshot model.GPTAccount
	if model.DB.First(&snapshot, accountID).Error != nil {
		return errors.New("account_not_found")
	}
	defer func() {
		model.DB.Model(&model.GPTAccount{}).Where("id = ? AND lease_id = ?", accountID, lease).Updates(map[string]any{"lease_id": "", "lease_until": 0})
		model.InitChannelCache()
	}()
	if !gptAccountOwnerActive(&snapshot) {
		return errors.New("authorization_forbidden")
	}
	var instance model.GPTAccountVM2APIInstance
	query := model.DB.Where("account_id = ?", accountID).Limit(1).Find(&instance)
	if query.Error != nil {
		return errors.New("operation_failed")
	}
	if query.RowsAffected > 0 {
		if err := model.StoreGPTAccountVM2APIProgress(&snapshot, map[string]any{"status": "pending_cleanup"}); err != nil {
			return err
		}
		if err := setManagedVM2APIChannelStatus(&snapshot, &instance, common.ChannelStatusAutoDisabled); err != nil {
			return err
		}
		if _, err := vm2apiManagerRequest(ctx, http.MethodDelete, instance.InstanceID, ""); err != nil {
			return err
		}
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.LockGPTAccountInstanceJob(tx, &snapshot); err != nil {
			return err
		}
		if instance.GatewayID != nil {
			var gateway model.GPTAccountGateway
			if tx.First(&gateway, *instance.GatewayID).Error != nil || gateway.ManagedAccountID == nil || *gateway.ManagedAccountID != accountID || gateway.ChannelID == nil {
				return model.ErrGPTAccountStale
			}
			if err := tx.Where("channel_id = ?", *gateway.ChannelID).Delete(&model.Ability{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&model.Channel{}, *gateway.ChannelID).Error; err != nil {
				return err
			}
			if err := tx.Delete(&gateway).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("account_id = ?", accountID).Delete(&model.GPTAccountVM2APIInstance{}).Error; err != nil {
			return err
		}
		return tx.Delete(&snapshot).Error
	})
}
