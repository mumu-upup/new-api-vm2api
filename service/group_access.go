package service

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var (
	ErrGroupAccessForbidden = errors.New("group access forbidden")
	ErrInvalidGroup         = errors.New("invalid group")
)

// GetUserUsableGroupsForUser returns legacy public groups and the user's active hidden grants.
func GetUserUsableGroupsForUser(userID int, userGroup string) (map[string]string, error) {
	groups := GetUserUsableGroups(userGroup)
	var metadata []model.GroupAccess
	if err := model.DB.Find(&metadata).Error; err != nil {
		return nil, err
	}
	access := make(map[string]model.GroupAccess, len(metadata))
	for _, item := range metadata {
		access[item.Name] = item
	}
	var grants []model.UserGroupGrant
	if userID > 0 {
		if err := model.DB.Where("user_id = ?", userID).Find(&grants).Error; err != nil {
			return nil, err
		}
	}
	for _, grant := range grants {
		item, ok := access[grant.GroupName]
		if ok && item.Hidden && item.Enabled && ratio_setting.ContainsGroupRatio(grant.GroupName) {
			groups[grant.GroupName] = setting.GetUsableGroupDescription(grant.GroupName)
		}
	}
	for name, item := range access {
		if item.Hidden || !item.Enabled {
			delete(groups, name)
		}
	}
	// Special user-group rules may explicitly add a hidden name; re-add only when granted.
	for _, grant := range grants {
		item, ok := access[grant.GroupName]
		if ok && item.Hidden && item.Enabled && ratio_setting.ContainsGroupRatio(grant.GroupName) {
			groups[grant.GroupName] = setting.GetUsableGroupDescription(grant.GroupName)
		}
	}
	for name := range groups {
		if !ratio_setting.ContainsGroupRatio(name) {
			delete(groups, name)
		}
	}
	return groups, nil
}

// ValidateUserGroupAccess checks a requested group against the user's effective grants.
func ValidateUserGroupAccess(userID int, groupName string) error {
	if groupName == "" {
		return ErrInvalidGroup
	}
	userGroup, lookupErr := userGroupForAccessValidation(userID)
	if lookupErr != nil {
		return fmt.Errorf("%w: %v", ErrGroupAccessForbidden, lookupErr)
	}
	groups, err := GetUserUsableGroupsForUser(userID, userGroup)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGroupAccessForbidden, err)
	}
	if groupName == "auto" {
		for _, candidate := range setting.GetAutoGroups() {
			if _, hidden := groups[candidate]; hidden {
				var metadata model.GroupAccess
				if model.DB.Where("name = ? AND hidden = ?", candidate, true).First(&metadata).Error == nil {
					return ErrGroupAccessForbidden
				}
			}
		}
		return nil
	}
	if _, ok := groups[groupName]; !ok || !ratio_setting.ContainsGroupRatio(groupName) {
		return ErrInvalidGroup
	}
	return nil
}

func userGroupForAccessValidation(userID int) (string, error) {
	if userID <= 0 || model.DB == nil {
		return "", nil
	}
	var user model.User
	if err := model.DB.Select("group").First(&user, userID).Error; err != nil {
		return "", err
	}
	return user.Group, nil
}
