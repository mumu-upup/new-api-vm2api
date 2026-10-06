package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

const GPTAccountDefaultGroupKey = "GPTAccountDefaultGroup"

func ValidateGPTAccountGroup(group string) error {
	if strings.TrimSpace(group) == "" || len(group) > 64 || strings.ContainsAny(group, "\r\n\x00") {
		return errors.New("invalid_account")
	}
	for name := range strings.SplitSeq(group, ",") {
		if strings.TrimSpace(name) == "" {
			return errors.New("invalid_account")
		}
	}
	return nil
}

// ResolveGPTAccountGroup reads persisted defaults at submission time so a
// change applies immediately across administrators and application nodes.
// Existing accounts retain the concrete routing group saved on creation.
func ResolveGPTAccountGroup(group string) (string, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		var option Option
		err := DB.Where(&Option{Key: GPTAccountDefaultGroupKey}).First(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			group = "default"
		} else if err != nil {
			return "", err
		} else {
			group = strings.TrimSpace(option.Value)
		}
	}
	if err := ValidateGPTAccountGroup(group); err != nil {
		return "", err
	}
	return group, nil
}

// GPTAccount contains management metadata. Recoverable login secrets never
// participate in JSON responses; channels retain their existing OAuth format.
type GPTAccount struct {
	ID                 int    `json:"id" gorm:"primaryKey"`
	OwnerID            int    `json:"-" gorm:"not null;index"`
	OwnerRole          int    `json:"-" gorm:"-"`
	Name               string `json:"name" gorm:"type:varchar(128);not null"`
	Email              string `json:"email" gorm:"type:varchar(254);not null;index"`
	ChannelID          *int   `json:"channel_id" gorm:"uniqueIndex"`
	ChannelName        string `json:"channel_name" gorm:"-"`
	GatewayID          *int   `json:"gateway_id" gorm:"index"`
	GatewayName        string `json:"gateway_name" gorm:"-"`
	GatewayProvider    string `json:"gateway_provider" gorm:"-"`
	GatewayChannelID   *int   `json:"gateway_channel_id" gorm:"-"`
	GatewayChannelName string `json:"gateway_channel_name" gorm:"-"`
	GatewaySyncStatus  string `json:"gateway_sync_status" gorm:"type:varchar(32)"`
	GatewayErrorCode   string `json:"gateway_error_code" gorm:"type:varchar(64)"`
	GatewaySyncedAt    int64  `json:"gateway_synced_at"`

	Group             string `json:"group" gorm:"column:account_group;type:varchar(64)"`
	Models            string `json:"models" gorm:"type:text"`
	AutoRelogin       bool   `json:"auto_relogin"`
	Status            string `json:"status" gorm:"type:varchar(32)"`
	LastErrorCode     string `json:"last_error_code" gorm:"type:varchar(64)"`
	LastLoginAt       int64  `json:"last_login_at"`
	LastAttemptAt     int64  `json:"last_attempt_at"`
	NextRetryAt       int64  `json:"next_retry_at"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime"`
	HasPassword       bool   `json:"has_password"`
	HasTOTP           bool   `json:"has_totp"`
	TOTPProvider      string `json:"totp_provider" gorm:"-"`
	TOTPVersion       int64  `json:"totp_version"`
	TOTPUpdatedAt     int64  `json:"totp_updated_at"`
	TOTPStatus        string `json:"totp_status" gorm:"type:varchar(32)"`
	CanManage         bool   `json:"can_manage" gorm:"-"`
	SecretID          string `json:"-" gorm:"type:varchar(64);not null"`
	SecretsCiphertext string `json:"-" gorm:"type:text;not null"`
	AccountID         string `json:"-" gorm:"type:varchar(128)"`
	OAuthSubject      string `json:"-" gorm:"type:varchar(255)"`
	ChannelKeyHash    string `json:"-" gorm:"type:char(64)"`
	Generation        int64  `json:"-" gorm:"not null"`
	LeaseID           string `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil        int64  `json:"-"`
	FailureCount      int    `json:"-"`
}

var ErrGPTAccountStale = errors.New("account_changed")
var ErrGPTAccountImportConflict = errors.New("account_conflict")

const GPTAccountRecoveryReason = "Managed account authorization in progress"

func GPTChannelKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// StoreGPTAccountCredential fences every external result against account edits,
// job takeover, channel replacement and manual disabling in the same transaction.
func StoreGPTAccountCredential(snapshot *GPTAccount, key, externalAccountID string) error {
	if snapshot.GatewayID != nil {
		return ErrGPTAccountStale
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var owner User
		if err := lockForUpdate(tx).First(&owner, snapshot.OwnerID).Error; err != nil || owner.Status != common.UserStatusEnabled || owner.Role < common.RoleAdminUser {
			return ErrGPTAccountStale
		}
		var account GPTAccount
		if err := lockForUpdate(tx).First(&account, snapshot.ID).Error; err != nil {
			return err
		}
		if account.GatewayID != nil || account.OwnerID != snapshot.OwnerID || account.Generation != snapshot.Generation || account.LeaseID != snapshot.LeaseID || account.LeaseUntil <= time.Now().Unix() || account.ChannelKeyHash != snapshot.ChannelKeyHash || (account.AccountID != "" && account.AccountID != externalAccountID) || (account.OAuthSubject != "" && account.OAuthSubject != snapshot.OAuthSubject) {
			return ErrGPTAccountStale
		}
		var channel Channel
		if account.ChannelID != nil {
			if err := lockForUpdate(tx).First(&channel, *account.ChannelID).Error; err != nil {
				return err
			}
			if channel.Type != constant.ChannelTypeCodex || channel.ChannelInfo.IsMultiKey || channel.Status == common.ChannelStatusManuallyDisabled || GPTChannelKeyHash(channel.Key) != snapshot.ChannelKeyHash {
				return ErrGPTAccountStale
			}
			updates := map[string]any{"key": key}
			if channel.Status == common.ChannelStatusAutoDisabled && channel.GetOtherInfo()["status_reason"] == GPTAccountRecoveryReason {
				channel.Status = common.ChannelStatusEnabled
				info := channel.GetOtherInfo()
				info["status_reason"] = ""
				channel.SetOtherInfo(info)
				updates["status"], updates["other_info"] = channel.Status, channel.OtherInfo
				if err := channel.UpdateAbilities(tx); err != nil {
					return err
				}
			}
			if err := tx.Model(&channel).Updates(updates).Error; err != nil {
				return err
			}
		} else {
			channel = Channel{Type: constant.ChannelTypeCodex, Key: key, Name: account.Name, Status: common.ChannelStatusEnabled, Group: account.Group, Models: account.Models, CreatedTime: time.Now().Unix()}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			if err := channel.AddAbilities(tx); err != nil {
				return err
			}
		}
		return tx.Model(&account).Updates(map[string]any{"channel_id": channel.Id, "channel_key_hash": GPTChannelKeyHash(key), "account_id": externalAccountID, "o_auth_subject": snapshot.OAuthSubject, "status": "ready", "last_error_code": "", "last_login_at": time.Now().Unix(), "failure_count": 0, "updated_at": time.Now().Unix()}).Error
	})
}

// CreateGPTAccountCredential atomically creates a new account and its Codex
// channel. The owner row serializes same-owner imports so duplicate email
// submissions cannot create a second account between lookup and insert.
// If a matching account already exists, it is returned with created=false for
// the caller to process through the normal fenced update path.
func CreateGPTAccountCredential(account *GPTAccount, key, externalAccountID string) (*GPTAccount, bool, error) {
	var result GPTAccount
	created := false
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		result = GPTAccount{}
		created = false
		err = DB.Transaction(func(tx *gorm.DB) error {
			var owner User
			if err := lockForUpdate(tx).First(&owner, account.OwnerID).Error; err != nil || owner.Status != common.UserStatusEnabled || owner.Role < common.RoleAdminUser {
				return ErrGPTAccountStale
			}
			var matches []GPTAccount
			if err := lockForUpdate(tx).Where("owner_id = ? AND email = ?", account.OwnerID, account.Email).Order("id ASC").Find(&matches).Error; err != nil {
				return err
			}
			for _, match := range matches {
				if (match.AccountID != "" && match.AccountID == externalAccountID) || (match.OAuthSubject != "" && match.OAuthSubject == account.OAuthSubject) {
					result = match
					return nil
				}
			}
			if len(matches) == 1 {
				result = matches[0]
				return nil
			}
			if len(matches) > 1 {
				return ErrGPTAccountImportConflict
			}
			if err := tx.Create(account).Error; err != nil {
				return err
			}
			channel := Channel{Type: constant.ChannelTypeCodex, Key: key, Name: account.Name, Status: common.ChannelStatusEnabled, Group: account.Group, Models: account.Models, CreatedTime: time.Now().Unix()}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			if err := channel.AddAbilities(tx); err != nil {
				return err
			}
			now := time.Now().Unix()
			if err := tx.Model(account).Updates(map[string]any{
				"channel_id": channel.Id, "channel_key_hash": GPTChannelKeyHash(key),
				"account_id": externalAccountID, "o_auth_subject": account.OAuthSubject,
				"status": "ready", "last_error_code": "", "last_login_at": now,
				"failure_count": 0, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			account.ChannelID = &channel.Id
			account.ChannelKeyHash = GPTChannelKeyHash(key)
			account.AccountID = externalAccountID
			account.Status = "ready"
			account.LastLoginAt = now
			result = *account
			created = true
			return nil
		})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") || attempt == 4 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return nil, false, err
	}
	return &result, created, nil
}

// PauseGPTAccountChannel suspends routing during an accepted recovery job. The
// exact reason distinguishes our temporary suspension from unrelated disables.
func PauseGPTAccountChannel(snapshot *GPTAccount) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var account GPTAccount
		if err := lockForUpdate(tx).First(&account, snapshot.ID).Error; err != nil {
			return err
		}
		if account.Generation != snapshot.Generation || account.LeaseID != snapshot.LeaseID || account.LeaseUntil <= time.Now().Unix() || account.ChannelID == nil {
			return ErrGPTAccountStale
		}
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, *account.ChannelID).Error; err != nil {
			return err
		}
		ownPause := channel.Status == common.ChannelStatusAutoDisabled && channel.GetOtherInfo()["status_reason"] == GPTAccountRecoveryReason
		if channel.Type != constant.ChannelTypeCodex || channel.ChannelInfo.IsMultiKey || (channel.Status != common.ChannelStatusEnabled && !ownPause) || GPTChannelKeyHash(channel.Key) != snapshot.ChannelKeyHash {
			return ErrGPTAccountStale
		}
		channel.Status = common.ChannelStatusAutoDisabled
		info := channel.GetOtherInfo()
		info["status_reason"], info["status_time"] = GPTAccountRecoveryReason, time.Now().Unix()
		channel.SetOtherInfo(info)
		if err := tx.Model(&channel).Updates(map[string]any{"status": channel.Status, "other_info": channel.OtherInfo}).Error; err != nil {
			return err
		}
		return channel.UpdateAbilities(tx)
	})
}
