package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const MaxUserConcurrentRequests = 2147483647

var ErrUserConcurrencyPermission = errors.New("cannot manage this user's concurrency limit")

func GetUserConcurrencyLimit(userID int) (int, error) {
	var user User
	if err := DB.Select("id", "max_concurrent_requests").First(&user, userID).Error; err != nil {
		return 0, err
	}
	return user.MaxConcurrentRequests, nil
}

// UpdateUserConcurrencyLimit checks hierarchy against the locked target row so
// a concurrent promotion cannot authorize a lower-ranked administrator's edit.
func UpdateUserConcurrencyLimit(userID, actorRole, limit int) error {
	if userID <= 0 || limit < 0 || limit > MaxUserConcurrentRequests {
		return errors.New("invalid concurrency limit")
	}
	if actorRole < common.RoleAdminUser {
		return ErrUserConcurrencyPermission
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id", "role").First(&user, userID).Error; err != nil {
			return err
		}
		if actorRole != common.RoleRootUser && actorRole <= user.Role {
			return ErrUserConcurrencyPermission
		}
		return tx.Model(&user).Update("max_concurrent_requests", limit).Error
	})
}
