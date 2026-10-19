package controller

import (
	"context"
	"encoding/base32"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type gptAccountInput struct {
	Name         string  `json:"name"`
	Email        string  `json:"email"`
	Password     string  `json:"password"`
	TOTPSecret   string  `json:"totp_secret"`
	TOTPProvider *string `json:"totp_provider"`
	AutoRelogin  *bool   `json:"auto_relogin"`
	Group        string  `json:"group"`
	Models       string  `json:"models"`
	ChannelID    *int    `json:"channel_id"`
	GatewayID    *int    `json:"gateway_id"`
}

type gptAccountOAuthImportRequest struct {
	OAuthJSON   string `json:"oauth_json"`
	Name        string `json:"name"`
	Group       string `json:"group"`
	Models      string `json:"models"`
	AutoRelogin *bool  `json:"auto_relogin"`
	GatewayID   *int   `json:"gateway_id"`
}

func gptAccountError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"success": false, "message": code, "code": code})
}

func gptAccountIdentity(c *gin.Context, manage bool) (service.AuthIdentity, bool) {
	permission := authz.GPTAccountRead
	if manage {
		permission = authz.GPTAccountManage
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetInt("role") < common.RoleAdminUser || !authz.Can(c.GetInt("id"), c.GetInt("role"), permission) {
		gptAccountError(c, http.StatusForbidden, "authorization_forbidden")
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func getManagedGPTAccount(c *gin.Context) (*model.GPTAccount, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	query := model.DB.Where("id = ?", id)
	if c.GetInt("role") != common.RoleRootUser {
		query = query.Where("owner_id = ?", c.GetInt("id"))
	}
	var account model.GPTAccount
	if query.First(&account).Error != nil {
		gptAccountError(c, http.StatusNotFound, "account_not_found")
		return nil, false
	}
	return &account, true
}

// populateGPTAccountChannels reads channel metadata from the database so a
// deleted channel cannot remain visible through the optional channel cache.
// A missing channel is reported to operators without mutating the account.
func populateGPTAccountChannels(accounts []model.GPTAccount) error {
	ids := make([]int, 0, len(accounts))
	seen := make(map[int]struct{}, len(accounts))
	for _, account := range accounts {
		if account.ChannelID == nil {
			continue
		}
		id := *account.ChannelID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	var channels []struct {
		ID   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := model.DB.Table("channels").Select("id", "name").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(channels))
	for _, channel := range channels {
		names[channel.ID] = channel.Name
	}
	for i := range accounts {
		if accounts[i].ChannelID == nil {
			continue
		}
		channelID := *accounts[i].ChannelID
		if name, ok := names[channelID]; ok {
			accounts[i].ChannelName = name
			continue
		}
		if accounts[i].Status == "ready" {
			accounts[i].Status = "needs_attention"
			accounts[i].LastErrorCode = "channel_missing"
		}
	}
	return nil
}

func gptAccountResponse(c *gin.Context, accountID int) {
	_, _ = service.ReclaimExpiredGPTAccountJobs(time.Now().Unix())
	var account model.GPTAccount
	if model.DB.First(&account, accountID).Error != nil {
		gptAccountError(c, http.StatusNotFound, "account_not_found")
		return
	}
	account.CanManage = true
	if secrets, err := service.DecryptGPTAccountSecrets(&account); err == nil {
		account.TOTPProvider = secrets.TOTPProvider
		if account.TOTPProvider == "" {
			account.TOTPProvider = "local"
		}
	}
	if account.HasTOTP && account.TOTPStatus == "" {
		account.TOTPStatus = "saved"
	}
	if account.Status == "logging_in" && account.LeaseUntil <= time.Now().Unix() {
		account.Status, account.LastErrorCode = "needs_attention", "login_timeout"
	}
	items := []model.GPTAccount{account}
	if err := populateGPTAccountChannels(items); err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	if service.PopulateGPTAccountGateways(items) != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": items[0]})
}

func ListGPTAccounts(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, false); !ok {
		return
	}
	_, _ = service.ReclaimExpiredGPTAccountJobs(time.Now().Unix())
	page, _ := strconv.Atoi(c.Query("p"))
	size, _ := strconv.Atoi(c.Query("page_size"))
	if size == 0 {
		size = 20
	}
	page, size = max(1, page), min(100, max(1, size))
	query := model.DB.Model(&model.GPTAccount{})
	if c.GetInt("role") != common.RoleRootUser {
		query = query.Where("owner_id = ?", c.GetInt("id"))
	}
	var total int64
	if query.Count(&total).Error != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	items := make([]model.GPTAccount, 0)
	if query.Order("id DESC").Offset((page-1)*size).Limit(size).Find(&items).Error != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	for i := range items {
		if secrets, err := service.DecryptGPTAccountSecrets(&items[i]); err == nil {
			items[i].TOTPProvider = secrets.TOTPProvider
			if items[i].TOTPProvider == "" {
				items[i].TOTPProvider = "local"
			}
		}
		if items[i].HasTOTP && items[i].TOTPStatus == "" {
			items[i].TOTPStatus = "saved"
		}
		if items[i].Status == "logging_in" && items[i].LeaseUntil <= time.Now().Unix() {
			items[i].Status, items[i].LastErrorCode = "needs_attention", "login_timeout"
		}
		items[i].CanManage = authz.Can(c.GetInt("id"), c.GetInt("role"), authz.GPTAccountManage)
	}
	if err := populateGPTAccountChannels(items); err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	if service.PopulateGPTAccountGateways(items) != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"items": items, "total": total}})
}

func GPTAccountsConfig(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, false); !ok {
		return
	}
	group, err := model.ResolveGPTAccountGroup("")
	if err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	models, err := model.ResolveGPTAccountModels("")
	if err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	defaultGateway, err := model.ResolveGPTAccountDefaultGateway()
	if err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"encryption_available": service.GPTAccountEncryptionAvailable(), "automation_available": service.GPTAccountAutomationAvailable(), "default_group": group, "default_models": models, "default_gateway_id": defaultGateway}})
}

func UpdateGPTAccountsConfig(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	if c.GetInt("role") != common.RoleRootUser {
		gptAccountError(c, http.StatusForbidden, "authorization_forbidden")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		DefaultGroup     *string `json:"default_group"`
		DefaultModels    *string `json:"default_models"`
		DefaultGatewayID *int    `json:"default_gateway_id"`
	}
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return
	}
	updates := make(map[string]string, 3)
	if input.DefaultGroup != nil {
		group := strings.TrimSpace(*input.DefaultGroup)
		if model.ValidateGPTAccountGroup(group) != nil || !ratio_setting.ContainsGroupRatio(group) {
			gptAccountError(c, http.StatusBadRequest, "invalid_account")
			return
		}
		updates[model.GPTAccountDefaultGroupKey] = group
	}
	if input.DefaultModels != nil {
		models, err := model.NormalizeGPTAccountDefaultModels(*input.DefaultModels)
		if err != nil {
			gptAccountError(c, http.StatusBadRequest, "invalid_account")
			return
		}
		updates[model.GPTAccountDefaultModelsKey] = models
	}
	if input.DefaultGatewayID != nil {
		if *input.DefaultGatewayID < 0 {
			gptAccountError(c, http.StatusBadRequest, "invalid_gateway")
			return
		}
		if *input.DefaultGatewayID > 0 {
			var gateway model.GPTAccountGateway
			if model.DB.Where("id = ? AND provider = ? AND managed_account_id IS NULL", *input.DefaultGatewayID, "vm2api").First(&gateway).Error != nil {
				gptAccountError(c, http.StatusBadRequest, "gateway_not_found")
				return
			}
			updates[model.GPTAccountDefaultGatewayKey] = strconv.Itoa(*input.DefaultGatewayID)
		} else {
			if err := model.DB.Where("key = ?", model.GPTAccountDefaultGatewayKey).Delete(&model.Option{}).Error; err != nil {
				gptAccountError(c, http.StatusInternalServerError, "operation_failed")
				return
			}
		}
	}
	if len(updates) == 0 {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return
	}
	if err := model.UpdateOptionsBulk(updates); err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return
	}
	recordManageAudit(c, "gpt_account.config.update", map[string]any{"default_group": input.DefaultGroup, "default_models": input.DefaultModels, "default_gateway_id": input.DefaultGatewayID})
	GPTAccountsConfig(c)
}

func normalizeGPTAccountInput(c *gin.Context, existing *model.GPTAccount) (*gptAccountInput, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input gptAccountInput
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	input.Name, input.Email, input.Group, input.Models = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Email)), strings.TrimSpace(input.Group), strings.TrimSpace(input.Models)
	if existing != nil {
		if input.Group == "" {
			input.Group = existing.Group
		}
		if input.Models == "" {
			input.Models = existing.Models
		}
	}
	var err error
	input.Group, err = model.ResolveGPTAccountGroup(input.Group)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	if existing == nil || input.Models == "" {
		input.Models, err = model.ResolveGPTAccountModels(input.Models)
		if err != nil {
			gptAccountError(c, http.StatusBadRequest, "invalid_account")
			return nil, false
		}
	}
	if input.Name == "" || len(input.Name) > 128 || len(input.Email) > 254 || common.Validate.Var(input.Email, "required,email") != nil || len(input.Password) > 1024 || len(input.Group) > 64 || strings.ContainsAny(input.Group, "\r\n") || len(input.Models) > 8192 || strings.ContainsAny(input.Models, "\r\n") {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	if input.TOTPSecret != "" {
		var valid bool
		input.TOTPSecret, valid = normalizeGPTAccountTOTPSecret(input.TOTPSecret)
		if !valid {
			gptAccountError(c, http.StatusBadRequest, "invalid_totp_secret")
			return nil, false
		}
	}
	if input.TOTPProvider != nil && *input.TOTPProvider != "local" && *input.TOTPProvider != "2fa_cn" {
		gptAccountError(c, http.StatusBadRequest, "invalid_totp_provider")
		return nil, false
	}
	return &input, true
}

func normalizeGPTAccountTOTPSecret(value string) (string, bool) {
	value = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	return value, err == nil && len(seed) >= 10 && len(value) <= 128 && base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed) == value
}

// UpdateGPTAccountTOTP records the operator's confirmation of an upstream binding.
// It does not enroll, change, or verify an authentication factor at the provider.
func UpdateGPTAccountTOTP(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var input struct {
		TOTPSecret     string `json:"totp_secret"`
		ConfirmedBound bool   `json:"confirmed_bound"`
	}
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_totp_secret")
		return
	}
	if !input.ConfirmedBound {
		gptAccountError(c, http.StatusBadRequest, "totp_confirmation_required")
		return
	}
	secret, valid := normalizeGPTAccountTOTPSecret(input.TOTPSecret)
	if !valid {
		gptAccountError(c, http.StatusBadRequest, "invalid_totp_secret")
		return
	}
	secrets, err := service.DecryptGPTAccountSecrets(account)
	if err != nil {
		gptAccountError(c, http.StatusServiceUnavailable, "encryption_unavailable")
		return
	}
	secrets.TOTPSecret = secret
	if service.EncryptGPTAccountSecrets(account, *secrets) != nil {
		gptAccountError(c, http.StatusServiceUnavailable, "encryption_unavailable")
		return
	}
	now := time.Now().Unix()
	// One conditional write preserves concurrent password edits and fences any
	// browser or OAuth result obtained before this secret was saved.
	result := model.DB.Model(&model.GPTAccount{}).Where("id = ? AND owner_id = ? AND generation = ?", account.ID, account.OwnerID, account.Generation).Updates(map[string]any{
		"secrets_ciphertext": account.SecretsCiphertext, "has_totp": true,
		"totp_version": account.TOTPVersion + 1, "totp_updated_at": now, "totp_status": "operator_confirmed",
		"generation": account.Generation + 1, "lease_id": "", "lease_until": 0,
		"failure_count": 0, "next_retry_at": 0, "status": "pending", "last_error_code": "", "updated_at": now,
	})
	if result.Error != nil || result.RowsAffected != 1 {
		gptAccountError(c, http.StatusConflict, "account_changed")
		return
	}
	recordManageAudit(c, "gpt_account.totp.update", map[string]any{"account_id": account.ID, "totp_version": account.TOTPVersion + 1})
	gptAccountResponse(c, account.ID)
}

// ChangeGPTAccountTOTP performs an administrator-triggered rotation. The
// worker returns only the outcome; the replacement secret is never sent to the
// browser or written to an audit record.
func ChangeGPTAccountTOTP(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	result, err := service.ChangeGPTAccountTOTP(c.Request.Context(), account.ID, identity)
	if err != nil {
		code := service.GPTAccountErrorCode(err)
		status := http.StatusConflict
		if code == "credentials_required" || code == "invalid_totp_secret" {
			status = http.StatusBadRequest
		} else if code == "encryption_unavailable" || code == "automation_unavailable" {
			status = http.StatusServiceUnavailable
		}
		gptAccountError(c, status, code)
		return
	}
	recordManageAudit(c, "gpt_account.totp.change", map[string]any{"account_id": account.ID, "stage": result.Stage, "fallback_used": result.FallbackUsed})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
}

func RevealGPTAccountTOTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	// Recheck ownership and the current secret immediately before disclosure.
	account, ok = getManagedGPTAccount(c)
	if !ok {
		return
	}
	secrets, err := service.DecryptGPTAccountSecrets(account)
	if err != nil {
		gptAccountError(c, http.StatusServiceUnavailable, "encryption_unavailable")
		return
	}
	if secrets.TOTPSecret == "" {
		gptAccountError(c, http.StatusNotFound, "totp_secret_not_found")
		return
	}
	recordManageAudit(c, "gpt_account.totp.read", map[string]any{"account_id": account.ID, "totp_version": account.TOTPVersion})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"totp_secret": secrets.TOTPSecret, "expires_at": time.Now().Add(30 * time.Second).Unix()}})
}

func applyGPTAccountInput(c *gin.Context, account *model.GPTAccount, input *gptAccountInput) bool {
	if account.ChannelID != nil && (input.Group != account.Group || input.Models != account.Models) {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return false
	}
	if (account.ChannelID != nil || account.AccountID != "") && input.Email != account.Email {
		gptAccountError(c, http.StatusBadRequest, "identity_mismatch")
		return false
	}
	if input.ChannelID != nil && (account.ChannelID == nil || *input.ChannelID != *account.ChannelID) {
		if c.GetInt("role") != common.RoleRootUser || !authz.Can(c.GetInt("id"), c.GetInt("role"), authz.ChannelSensitiveWrite) || account.ChannelID != nil {
			gptAccountError(c, http.StatusForbidden, "authorization_forbidden")
			return false
		}
		channel, err := model.GetChannelById(*input.ChannelID, true)
		if err != nil || channel.Type != constant.ChannelTypeCodex || channel.ChannelInfo.IsMultiKey {
			gptAccountError(c, http.StatusBadRequest, "invalid_channel")
			return false
		}
		var key service.CodexOAuthKey
		if common.UnmarshalJsonStr(channel.Key, &key) != nil || key.AccountID == "" || !strings.EqualFold(strings.TrimSpace(key.Email), input.Email) {
			gptAccountError(c, http.StatusBadRequest, "identity_mismatch")
			return false
		}
		account.ChannelID, account.ChannelKeyHash, account.AccountID = input.ChannelID, model.GPTChannelKeyHash(channel.Key), key.AccountID
	}
	if account.GatewayID != nil && account.ChannelID != nil {
		gptAccountError(c, http.StatusBadRequest, "gateway_immutable")
		return false
	}
	secrets := &service.GPTAccountSecrets{}
	if account.SecretsCiphertext != "" {
		var err error
		secrets, err = service.DecryptGPTAccountSecrets(account)
		if err != nil {
			gptAccountError(c, http.StatusServiceUnavailable, "encryption_unavailable")
			return false
		}
	}
	if input.Password != "" {
		secrets.Password = input.Password
	}
	if input.TOTPProvider != nil {
		secrets.TOTPProvider = *input.TOTPProvider
	}
	if input.TOTPSecret != "" && input.TOTPSecret != secrets.TOTPSecret {
		secrets.TOTPSecret = input.TOTPSecret
		account.TOTPVersion++
		account.TOTPUpdatedAt = time.Now().Unix()
		account.TOTPStatus = "saved"
	}
	if err := service.EncryptGPTAccountSecrets(account, *secrets); err != nil {
		gptAccountError(c, http.StatusServiceUnavailable, "encryption_unavailable")
		return false
	}
	account.Name, account.Email, account.Group, account.Models = input.Name, input.Email, input.Group, input.Models
	if err := service.ApplyGPTAccountGateway(account, input.GatewayID); err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return false
	}
	if input.AutoRelogin != nil {
		account.AutoRelogin = *input.AutoRelogin
	}
	return true
}

func CreateGPTAccount(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	input, ok := normalizeGPTAccountInput(c, nil)
	if !ok {
		return
	}
	account := model.GPTAccount{OwnerID: identity.UserID, SecretID: uuid.NewString(), Generation: 1, Status: "pending", AutoRelogin: true}
	if !applyGPTAccountInput(c, &account, input) {
		return
	}
	if model.DB.Create(&account).Error != nil {
		gptAccountError(c, http.StatusConflict, "account_conflict")
		return
	}
	recordManageAudit(c, "gpt_account.create", map[string]any{"account_id": account.ID})
	_ = service.StartGPTAccountLogin(account.ID, identity, false)
	gptAccountResponse(c, account.ID)
}

func UpdateGPTAccount(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	if account.LeaseUntil > time.Now().Unix() {
		gptAccountError(c, http.StatusConflict, "account_busy")
		return
	}
	input, ok := normalizeGPTAccountInput(c, account)
	if !ok {
		return
	}
	if !applyGPTAccountInput(c, account, input) {
		return
	}
	result := model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ? AND lease_until <= ?", account.ID, account.Generation, time.Now().Unix()).Updates(map[string]any{"name": account.Name, "email": account.Email, "account_group": account.Group, "models": account.Models, "auto_relogin": account.AutoRelogin, "channel_id": account.ChannelID, "channel_key_hash": account.ChannelKeyHash, "account_id": account.AccountID, "secrets_ciphertext": account.SecretsCiphertext, "has_password": account.HasPassword, "has_totp": account.HasTOTP, "totp_version": account.TOTPVersion, "totp_updated_at": account.TOTPUpdatedAt, "totp_status": account.TOTPStatus, "generation": account.Generation + 1, "lease_id": "", "lease_until": 0, "failure_count": 0, "next_retry_at": 0, "status": "pending", "last_error_code": "", "updated_at": time.Now().Unix()})
	if result.Error != nil || result.RowsAffected != 1 {
		gptAccountError(c, http.StatusConflict, "account_changed")
		return
	}
	recordManageAudit(c, "gpt_account.update", map[string]any{"account_id": account.ID})
	if input.Password != "" || input.TOTPSecret != "" {
		if err := service.StartGPTAccountLogin(account.ID, identity, false); err != nil && service.GPTAccountErrorCode(err) != "automation_unavailable" {
			gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
			return
		}
	}
	gptAccountResponse(c, account.ID)
}

func DeleteGPTAccount(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	if err := service.DeleteGPTAccountWithInstance(c.Request.Context(), account.ID); err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.delete", map[string]any{"account_id": account.ID})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func LoginGPTAccount(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	_, _ = service.ReclaimExpiredGPTAccountJobs(time.Now().Unix())
	if model.DB.First(account, account.ID).Error != nil {
		gptAccountError(c, http.StatusNotFound, "account_not_found")
		return
	}
	if err := service.StartGPTAccountLogin(account.ID, identity, false); err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.login", map[string]any{"account_id": account.ID})
	gptAccountResponse(c, account.ID)
}

func normalizeGPTAccountOAuthImport(c *gin.Context) (*service.GPTAccountOAuthImportInput, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var request gptAccountOAuthImportRequest
	if c.ShouldBindJSON(&request) != nil {
		gptAccountError(c, http.StatusBadRequest, "oauth_import_invalid")
		return nil, false
	}
	_, err := service.ParseCodexOAuthImport(request.OAuthJSON)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return nil, false
	}
	request.Name, request.Group, request.Models = strings.TrimSpace(request.Name), strings.TrimSpace(request.Group), strings.TrimSpace(request.Models)
	if len(request.Name) > 128 || len(request.Group) > 64 || len(request.Models) > 8192 || strings.ContainsAny(request.Group, "\r\n") || strings.ContainsAny(request.Models, "\r\n") {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	return &service.GPTAccountOAuthImportInput{OAuthJSON: request.OAuthJSON, Name: request.Name, Group: request.Group, Models: request.Models, AutoRelogin: request.AutoRelogin, GatewayID: request.GatewayID}, true
}

func gptAccountOAuthImportError(c *gin.Context, err error) {
	code := service.GPTAccountErrorCode(err)
	status := http.StatusBadRequest
	switch code {
	case "account_not_found":
		status = http.StatusNotFound
	case "account_conflict", "account_busy", "account_changed", "identity_mismatch", "gateway_immutable", "gateway_import_uncertain", "gateway_remote_conflict", "gateway_remote_missing":
		status = http.StatusConflict
	case "authorization_forbidden":
		status = http.StatusForbidden
	case "encryption_unavailable":
		status = http.StatusServiceUnavailable
	}
	gptAccountError(c, status, code)
}

func ImportGPTAccountOAuth(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	input, ok := normalizeGPTAccountOAuthImport(c)
	if !ok {
		return
	}
	account, err := service.ImportGPTAccountOAuth(c.Request.Context(), identity.UserID, 0, *input)
	if err != nil {
		gptAccountOAuthImportError(c, err)
		return
	}
	audit := map[string]any{"account_id": account.ID}
	if account.ChannelID != nil {
		audit["channel_id"] = *account.ChannelID
	}
	recordManageAudit(c, "gpt_account.oauth_import", audit)
	gptAccountResponse(c, account.ID)
}

func ImportGPTAccountOAuthForAccount(c *gin.Context) {
	_, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	input, ok := normalizeGPTAccountOAuthImport(c)
	if !ok {
		return
	}
	// Root operators may manage an account owned by another administrator;
	// import must fence against the account's owner, not the acting session.
	updated, err := service.ImportGPTAccountOAuth(c.Request.Context(), account.OwnerID, account.ID, *input)
	if err != nil {
		gptAccountOAuthImportError(c, err)
		return
	}
	audit := map[string]any{"account_id": updated.ID}
	if updated.ChannelID != nil {
		audit["channel_id"] = *updated.ChannelID
	}
	recordManageAudit(c, "gpt_account.oauth_import", audit)
	gptAccountResponse(c, updated.ID)
}

func StartGPTAccountOAuth(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	result, err := service.StartGPTAccountAuthorization(account.ID, identity)
	if err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.oauth", map[string]any{"account_id": account.ID})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": result})
}

func CompleteGPTAccountOAuth(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input struct {
		RedirectURL string `json:"redirect_url"`
	}
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "authorization_invalid")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	if err := service.FinishGPTAccountAuthorization(ctx, account.ID, identity, input.RedirectURL); err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.callback", map[string]any{"account_id": account.ID})
	gptAccountResponse(c, account.ID)
}

func RefreshGPTAccount(c *gin.Context) {
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if _, _, err := service.RefreshManagedGPTAccount(ctx, account.ID); err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.refresh", map[string]any{"account_id": account.ID})
	gptAccountResponse(c, account.ID)
}
