package model

import (
	"time"

	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// GroupAccess stores visibility and availability metadata for a billing group.
type GroupAccess struct {
	Name      string `gorm:"primaryKey;type:varchar(128)" json:"name"`
	Hidden    bool   `gorm:"not null" json:"hidden"`
	Enabled   bool   `gorm:"not null" json:"enabled"`
	UpdatedAt int64  `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

// UserGroupGrant grants one user access to a hidden group.
type UserGroupGrant struct {
	UserID    int    `gorm:"primaryKey;column:user_id" json:"user_id"`
	GroupName string `gorm:"primaryKey;type:varchar(128);column:group_name" json:"group_name"`
	GrantedBy int    `gorm:"not null;column:granted_by" json:"granted_by"`
	CreatedAt int64  `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

func (UserGroupGrant) TableName() string { return "user_group_grants" }
func (GroupAccess) TableName() string    { return "group_accesses" }

// ReplaceUserGroupGrants atomically replaces all grants for a user.
func ReplaceUserGroupGrants(tx *gorm.DB, userID int, groups []string, grantedBy int) error {
	if tx == nil {
		tx = DB
	}
	now := time.Now().Unix()
	err := tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&UserGroupGrant{}).Error; err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(groups))
		for _, name := range groups {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			if err := tx.Create(&UserGroupGrant{UserID: userID, GroupName: name, GrantedBy: grantedBy, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		_ = invalidateUserCache(userID)
	}
	return err
}

// SeedGroupAccessMetadata records legacy groups as public and enabled.
func SeedGroupAccessMetadata() error {
	if DB == nil {
		return nil
	}
	groups := setting.GetUserUsableGroupsCopy()
	for name := range ratio_setting.GetGroupRatioCopy() {
		groups[name] = name
	}
	for name := range groups {
		result := DB.Where("name = ?", name).FirstOrCreate(&GroupAccess{Name: name, Hidden: false, Enabled: true})
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}
