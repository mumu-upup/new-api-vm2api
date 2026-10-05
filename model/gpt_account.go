package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// GPTAccount contains management metadata. Recoverable login secrets never
// participate in JSON responses; channels retain their existing OAuth format.
type GPTAccount struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	OwnerID           int    `json:"-" gorm:"not null;index"`
	OwnerRole         int    `json:"-" gorm:"-"`
	Name              string `json:"name" gorm:"type:varchar(128);not null"`
	Email             string `json:"email" gorm:"type:varchar(254);not null;index"`
	ChannelID         *int   `json:"channel_id" gorm:"uniqueIndex"`
	ChannelName       string `json:"channel_name" gorm:"-"`
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

const GPTAccountRecoveryReason = "Managed account authorization in progress"

func GPTChannelKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// StoreGPTAccountCredential fences every external result against account edits,
// job takeover, channel replacement and manual disabling in the same transaction.
func StoreGPTAccountCredential(snapshot *GPTAccount, key, externalAccountID string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var owner User
		if err := lockForUpdate(tx).First(&owner, snapshot.OwnerID).Error; err != nil || owner.Status != common.UserStatusEnabled || owner.Role < common.RoleAdminUser {
			return ErrGPTAccountStale
		}
		var account GPTAccount
		if err := lockForUpdate(tx).First(&account, snapshot.ID).Error; err != nil {
			return err
		}
		if account.OwnerID != snapshot.OwnerID || account.Generation != snapshot.Generation || account.LeaseID != snapshot.LeaseID || account.LeaseUntil <= time.Now().Unix() || account.ChannelKeyHash != snapshot.ChannelKeyHash || (account.AccountID != "" && account.AccountID != externalAccountID) || (account.OAuthSubject != "" && account.OAuthSubject != snapshot.OAuthSubject) {
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
