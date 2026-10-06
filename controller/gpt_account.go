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

func gptAccountProof(c *gin.Context, scope string, accountID int, email string) bool {
	var binding any = struct {
		AccountID int `json:"account_id"`
	}{accountID}
	if scope == "gpt_account.create" || scope == "gpt_account.oauth_import.create" {
		binding = struct {
			Email string `json:"email"`
		}{email}
	}
	encoded, err := common.Marshal(binding)
	if err != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
		return false
	}
	return middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: scope, Context: encoded}) != nil
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
	if account.ChannelID != nil {
		var channel model.Channel
		if model.DB.Select("id", "name").First(&channel, *account.ChannelID).Error == nil {
			account.ChannelName = channel.Name
		}
	}
	items := []model.GPTAccount{account}
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
	ids := make([]int, 0, len(items))
	for _, account := range items {
		if account.ChannelID != nil {
			ids = append(ids, *account.ChannelID)
		}
	}
	names, _ := model.GetChannelNamesByIds(ids)
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
		if items[i].ChannelID != nil {
			items[i].ChannelName = names[*items[i].ChannelID]
		}
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
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"encryption_available": service.GPTAccountEncryptionAvailable(), "automation_available": service.GPTAccountAutomationAvailable()}})
}

func normalizeGPTAccountInput(c *gin.Context) (*gptAccountInput, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input gptAccountInput
	if c.ShouldBindJSON(&input) != nil {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, false
	}
	input.Name, input.Email, input.Group, input.Models = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Email)), strings.TrimSpace(input.Group), strings.TrimSpace(input.Models)
	if input.Group == "" {
		input.Group = "default"
	}
	if input.Models == "" {
		input.Models = "gpt-5"
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
	if !gptAccountProof(c, "gpt_account.totp.update", account.ID, "") {
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
	if !gptAccountProof(c, "gpt_account.totp.read", account.ID, "") {
		return
	}
	// Proof verification can overlap a replacement; disclose the current stored
	// secret and recheck ownership after verification completes.
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
	input, ok := normalizeGPTAccountInput(c)
	if !ok {
		return
	}
	if !gptAccountProof(c, "gpt_account.create", 0, input.Email) {
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
	if _, ok := gptAccountIdentity(c, true); !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	input, ok := normalizeGPTAccountInput(c)
	if !ok {
		return
	}
	if !gptAccountProof(c, "gpt_account.update", account.ID, "") {
		return
	}
	if !applyGPTAccountInput(c, account, input) {
		return
	}
	result := model.DB.Model(&model.GPTAccount{}).Where("id = ? AND generation = ?", account.ID, account.Generation).Updates(map[string]any{"name": account.Name, "email": account.Email, "account_group": account.Group, "models": account.Models, "auto_relogin": account.AutoRelogin, "channel_id": account.ChannelID, "channel_key_hash": account.ChannelKeyHash, "account_id": account.AccountID, "secrets_ciphertext": account.SecretsCiphertext, "has_password": account.HasPassword, "has_totp": account.HasTOTP, "totp_version": account.TOTPVersion, "totp_updated_at": account.TOTPUpdatedAt, "totp_status": account.TOTPStatus, "generation": account.Generation + 1, "lease_id": "", "lease_until": 0, "failure_count": 0, "next_retry_at": 0, "status": "pending", "last_error_code": "", "updated_at": time.Now().Unix()})
	if result.Error != nil || result.RowsAffected != 1 {
		gptAccountError(c, http.StatusConflict, "account_changed")
		return
	}
	recordManageAudit(c, "gpt_account.update", map[string]any{"account_id": account.ID})
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
	if !gptAccountProof(c, "gpt_account.delete", account.ID, "") {
		return
	}
	if model.DB.Delete(account).Error != nil {
		gptAccountError(c, http.StatusInternalServerError, "operation_failed")
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
	if !gptAccountProof(c, "gpt_account.login", account.ID, "") {
		return
	}
	if err := service.StartGPTAccountLogin(account.ID, identity, false); err != nil {
		gptAccountError(c, http.StatusConflict, service.GPTAccountErrorCode(err))
		return
	}
	recordManageAudit(c, "gpt_account.login", map[string]any{"account_id": account.ID})
	gptAccountResponse(c, account.ID)
}

func normalizeGPTAccountOAuthImport(c *gin.Context) (*service.GPTAccountOAuthImportInput, *service.CodexOAuthKey, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var request gptAccountOAuthImportRequest
	if c.ShouldBindJSON(&request) != nil {
		gptAccountError(c, http.StatusBadRequest, "oauth_import_invalid")
		return nil, nil, false
	}
	key, err := service.ParseCodexOAuthImport(request.OAuthJSON)
	if err != nil {
		gptAccountError(c, http.StatusBadRequest, service.GPTAccountErrorCode(err))
		return nil, nil, false
	}
	request.Name, request.Group, request.Models = strings.TrimSpace(request.Name), strings.TrimSpace(request.Group), strings.TrimSpace(request.Models)
	if len(request.Name) > 128 || len(request.Group) > 64 || len(request.Models) > 8192 || strings.ContainsAny(request.Group, "\r\n") || strings.ContainsAny(request.Models, "\r\n") {
		gptAccountError(c, http.StatusBadRequest, "invalid_account")
		return nil, nil, false
	}
	return &service.GPTAccountOAuthImportInput{OAuthJSON: request.OAuthJSON, Name: request.Name, Group: request.Group, Models: request.Models, AutoRelogin: request.AutoRelogin}, &key, true
}

func gptAccountOAuthImportError(c *gin.Context, err error) {
	code := service.GPTAccountErrorCode(err)
	status := http.StatusBadRequest
	switch code {
	case "account_not_found":
		status = http.StatusNotFound
	case "account_conflict", "account_busy", "account_changed", "identity_mismatch", "gateway_immutable":
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
	input, key, ok := normalizeGPTAccountOAuthImport(c)
	if !ok {
		return
	}
	if !gptAccountProof(c, "gpt_account.oauth_import.create", 0, key.Email) {
		return
	}
	account, err := service.ImportGPTAccountOAuth(c.Request.Context(), identity.UserID, 0, *input)
	if err != nil {
		gptAccountOAuthImportError(c, err)
		return
	}
	recordManageAudit(c, "gpt_account.oauth_import", map[string]any{"account_id": account.ID})
	gptAccountResponse(c, account.ID)
}

func ImportGPTAccountOAuthForAccount(c *gin.Context) {
	identity, ok := gptAccountIdentity(c, true)
	if !ok {
		return
	}
	account, ok := getManagedGPTAccount(c)
	if !ok {
		return
	}
	input, _, ok := normalizeGPTAccountOAuthImport(c)
	if !ok {
		return
	}
	if !gptAccountProof(c, "gpt_account.oauth_import.update", account.ID, "") {
		return
	}
	updated, err := service.ImportGPTAccountOAuth(c.Request.Context(), identity.UserID, account.ID, *input)
	if err != nil {
		gptAccountOAuthImportError(c, err)
		return
	}
	recordManageAudit(c, "gpt_account.oauth_import", map[string]any{"account_id": updated.ID})
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
	if !gptAccountProof(c, "gpt_account.oauth", account.ID, "") {
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
	if !gptAccountProof(c, "gpt_account.refresh", account.ID, "") {
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
