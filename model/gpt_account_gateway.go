package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// GPTAccountGateway owns one shared pool channel; account membership is not a
// guarantee that a relay request is served by any particular imported account.
type GPTAccountGateway struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	Name              string `json:"name" gorm:"type:varchar(128);not null"`
	Provider          string `json:"provider" gorm:"type:varchar(16);not null"`
	ManagementVersion string `json:"management_version" gorm:"type:varchar(16)"`
	ManagementURL     string `json:"management_url" gorm:"type:varchar(2048);not null"`
	RelayURL          string `json:"relay_url" gorm:"type:varchar(2048);not null"`
	AllowLocalHTTP    bool   `json:"allow_local_http"`
	RemoteGroupID     int64  `json:"remote_group_id"`
	Group             string `json:"group" gorm:"column:gateway_group;type:varchar(64)"`
	Models            string `json:"models" gorm:"type:text"`
	ChannelID         *int   `json:"channel_id" gorm:"uniqueIndex"`
	ChannelName       string `json:"channel_name" gorm:"-"`
	HasManagementKey  bool   `json:"has_management_key"`
	HasRelayKey       bool   `json:"has_relay_key"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime"`
	SecretID          string `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"`
	SecretsCiphertext string `json:"-" gorm:"type:text;not null"`
	Generation        int64  `json:"-" gorm:"not null"`
}

// SaveGPTAccountGateway atomically keeps the gateway and its standard channel in
// agreement, and fences previously acquired account jobs on configuration edits.
func SaveGPTAccountGateway(gateway *GPTAccountGateway, relayKey string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if gateway.ID != 0 {
			var current GPTAccountGateway
			if err := lockForUpdate(tx).First(&current, gateway.ID).Error; err != nil {
				return err
			}
			if current.Generation != gateway.Generation || current.ChannelID == nil {
				return ErrGPTAccountStale
			}
			if err := lockForUpdate(tx).First(&channel, *current.ChannelID).Error; err != nil {
				return err
			}
			gateway.ChannelID = current.ChannelID
		}
		channelType := constant.ChannelTypeOpenAI
		if gateway.Provider == "sub2api" {
			channelType = constant.ChannelTypeSub2API
		}
		if channel.Id != 0 && (channel.Type != channelType || channel.ChannelInfo.IsMultiKey) {
			return ErrGPTAccountStale
		}
		channel.Name, channel.Key, channel.BaseURL, channel.Group, channel.Models = gateway.Name, relayKey, &gateway.RelayURL, gateway.Group, gateway.Models
		if channel.Id == 0 {
			channel.Type, channel.Status, channel.CreatedTime = channelType, common.ChannelStatusEnabled, time.Now().Unix()
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			if err := channel.AddAbilities(tx); err != nil {
				return err
			}
		} else {
			if err := tx.Model(&channel).Updates(map[string]any{"name": channel.Name, "key": channel.Key, "base_url": channel.BaseURL, "group": channel.Group, "models": channel.Models}).Error; err != nil {
				return err
			}
			if err := channel.UpdateAbilities(tx); err != nil {
				return err
			}
		}
		gateway.ChannelID, gateway.ChannelName = &channel.Id, channel.Name
		gateway.Generation++
		if gateway.ID == 0 {
			return tx.Create(gateway).Error
		}
		if err := tx.Model(&GPTAccountGateway{}).Where("id = ?", gateway.ID).Select("name", "gateway_group", "models", "secrets_ciphertext", "has_management_key", "has_relay_key", "generation", "updated_at").Updates(gateway).Error; err != nil {
			return err
		}
		return tx.Model(&GPTAccount{}).Where("gateway_id = ?", gateway.ID).Updates(map[string]any{"generation": gorm.Expr("generation + 1"), "lease_id": "", "lease_until": 0, "gateway_sync_status": "pending", "gateway_error_code": "", "status": "pending", "last_error_code": "", "updated_at": time.Now().Unix()}).Error
	})
}

// StoreGPTAccountGatewayCheckpoint fences local import intent/result persistence.
// Remote writes cannot be made atomic with this database transaction.
func StoreGPTAccountGatewayCheckpoint(snapshot *GPTAccount, gateway *GPTAccountGateway, ciphertext, status, code string, complete bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var currentGateway GPTAccountGateway
		if err := lockForUpdate(tx).First(&currentGateway, gateway.ID).Error; err != nil {
			return ErrGPTAccountStale
		}
		if currentGateway.Generation != gateway.Generation {
			return ErrGPTAccountStale
		}
		var owner User
		if err := lockForUpdate(tx).First(&owner, snapshot.OwnerID).Error; err != nil || owner.Status != common.UserStatusEnabled || owner.Role < common.RoleAdminUser {
			return ErrGPTAccountStale
		}
		var account GPTAccount
		if err := lockForUpdate(tx).First(&account, snapshot.ID).Error; err != nil {
			return ErrGPTAccountStale
		}
		if account.OwnerID != snapshot.OwnerID || account.GatewayID == nil || *account.GatewayID != gateway.ID || account.ChannelID != nil || account.Generation != snapshot.Generation || account.LeaseID != snapshot.LeaseID || account.LeaseUntil <= time.Now().Unix() || (account.AccountID != "" && account.AccountID != snapshot.AccountID) || (account.OAuthSubject != "" && account.OAuthSubject != snapshot.OAuthSubject) {
			return ErrGPTAccountStale
		}
		values := map[string]any{"secrets_ciphertext": ciphertext, "account_id": snapshot.AccountID, "o_auth_subject": snapshot.OAuthSubject, "gateway_sync_status": status, "gateway_error_code": code, "updated_at": time.Now().Unix()}
		if complete {
			values["gateway_synced_at"], values["status"], values["last_error_code"], values["failure_count"] = time.Now().Unix(), "ready", "", 0
			values["last_login_at"] = time.Now().Unix()
		}
		return tx.Model(&account).Updates(values).Error
	})
}
