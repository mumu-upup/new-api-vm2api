package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// GPTAccountVM2APIInstance is durable provisioning intent. Runtime keys are
// encrypted on the gateway; an in-flight migration package is also encrypted.
// Source bindings survive migration to support deliberate operator rollback.
type GPTAccountVM2APIInstance struct {
	AccountID               int    `json:"account_id" gorm:"primaryKey;autoIncrement:false"`
	InstanceID              string `json:"instance_id" gorm:"type:char(32);not null;uniqueIndex"`
	GatewayID               *int   `json:"gateway_id" gorm:"uniqueIndex"`
	SourceGatewayID         int    `json:"source_gateway_id"`
	Status                  string `json:"status" gorm:"type:varchar(32);not null"`
	ErrorCode               string `json:"error_code" gorm:"type:varchar(64)"`
	ContainerName           string `json:"container_name" gorm:"type:varchar(128)"`
	DataDirectory           string `json:"data_directory" gorm:"type:varchar(2048)"`
	Image                   string `json:"image" gorm:"type:varchar(512)"`
	Port                    int    `json:"port"`
	Migration               bool   `json:"-"`
	SourceStatus            string `json:"-" gorm:"type:varchar(32)"`
	SourceSchedulable       bool   `json:"-"`
	SourcePaused            bool   `json:"-"`
	ImportAttempted         bool   `json:"-"`
	PackageCiphertext       string `json:"-" gorm:"type:text"`
	SourceSecretsCiphertext string `json:"-" gorm:"type:text"`
	CreatedAt               int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt               int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// LockGPTAccountInstanceJob validates the same fencing values at every durable
// transition, including before publishing an enabled routing channel.
func LockGPTAccountInstanceJob(tx *gorm.DB, snapshot *GPTAccount) error {
	var owner User
	if err := lockForUpdate(tx).First(&owner, snapshot.OwnerID).Error; err != nil || owner.Status != common.UserStatusEnabled || owner.Role < common.RoleAdminUser {
		return ErrGPTAccountStale
	}
	var current GPTAccount
	if err := lockForUpdate(tx).First(&current, snapshot.ID).Error; err != nil {
		return ErrGPTAccountStale
	}
	if current.OwnerID != snapshot.OwnerID || current.Generation != snapshot.Generation || current.LeaseID != snapshot.LeaseID || current.LeaseUntil <= time.Now().Unix() {
		return ErrGPTAccountStale
	}
	if (current.GatewayID == nil) != (snapshot.GatewayID == nil) || (current.GatewayID != nil && *current.GatewayID != *snapshot.GatewayID) {
		return ErrGPTAccountStale
	}
	return nil
}

func RequestGPTAccountVM2APIInstance(snapshot *GPTAccount, migration bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := LockGPTAccountInstanceJob(tx, snapshot); err != nil {
			return err
		}
		var existing GPTAccountVM2APIInstance
		result := tx.Where("account_id = ?", snapshot.ID).Limit(1).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if existing.Status == "pending_cleanup" {
				return ErrGPTAccountStale
			}
			return nil
		}
		if snapshot.GatewayID == nil || snapshot.ChannelID != nil {
			return ErrGPTAccountStale
		}
		var source GPTAccountGateway
		if err := tx.First(&source, *snapshot.GatewayID).Error; err != nil || source.Provider != "vm2api" || source.ManagedAccountID != nil {
			return ErrGPTAccountStale
		}
		instance := GPTAccountVM2APIInstance{AccountID: snapshot.ID, InstanceID: GPTChannelKeyHash(snapshot.SecretID)[:32], SourceGatewayID: source.ID, Status: "pending", Migration: migration, SourceSecretsCiphertext: ""}
		if migration {
			instance.SourceSecretsCiphertext = snapshot.SecretsCiphertext
		}
		return tx.Create(&instance).Error
	})
}

// BindGPTAccountVM2APIInstance creates the dedicated disabled channel and
// switches the management binding in one transaction. No shared key is reused.
func BindGPTAccountVM2APIInstance(snapshot *GPTAccount, instance *GPTAccountVM2APIInstance, gateway *GPTAccountGateway, relayKey, accountCiphertext string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := LockGPTAccountInstanceJob(tx, snapshot); err != nil {
			return err
		}
		var current GPTAccountVM2APIInstance
		if err := lockForUpdate(tx).First(&current, "account_id = ?", snapshot.ID).Error; err != nil {
			return err
		}
		if current.GatewayID != nil || current.InstanceID != instance.InstanceID || current.Status == "pending_cleanup" {
			return ErrGPTAccountStale
		}
		channel := Channel{Type: constant.ChannelTypeOpenAI, Name: gateway.Name, Key: relayKey, BaseURL: &gateway.RelayURL, Group: gateway.Group, Models: gateway.Models, Status: common.ChannelStatusAutoDisabled, CreatedTime: time.Now().Unix()}
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		if err := channel.AddAbilities(tx); err != nil {
			return err
		}
		gateway.ChannelID = &channel.Id
		gateway.ManagedAccountID = &snapshot.ID
		gateway.Generation = 1
		if err := tx.Create(gateway).Error; err != nil {
			return err
		}
		if err := tx.Model(&current).Updates(map[string]any{"gateway_id": gateway.ID, "port": instance.Port, "container_name": instance.ContainerName, "data_directory": instance.DataDirectory, "image": instance.Image, "status": "syncing", "error_code": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&GPTAccount{}).Where("id = ?", snapshot.ID).Updates(map[string]any{"gateway_id": gateway.ID, "secrets_ciphertext": accountCiphertext, "gateway_sync_status": "pending", "status": "pending"}).Error; err != nil {
			return err
		}
		instance.GatewayID = &gateway.ID
		snapshot.GatewayID = &gateway.ID
		snapshot.SecretsCiphertext = accountCiphertext
		return nil
	})
}

func StoreGPTAccountVM2APIProgress(snapshot *GPTAccount, values map[string]any) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := LockGPTAccountInstanceJob(tx, snapshot); err != nil {
			return err
		}
		return tx.Model(&GPTAccountVM2APIInstance{}).Where("account_id = ?", snapshot.ID).Updates(values).Error
	})
}
