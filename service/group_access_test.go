package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetUserUsableGroupsForUserFiltersHiddenGroups(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, db.AutoMigrate(&model.GroupAccess{}, &model.UserGroupGrant{}))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"secret":2}`))
	require.NoError(t, db.Create(&model.GroupAccess{Name: "secret", Hidden: true, Enabled: true}).Error)
	groups, err := GetUserUsableGroupsForUser(9, "default")
	require.NoError(t, err)
	require.NotContains(t, groups, "secret")
	require.NoError(t, model.ReplaceUserGroupGrants(db, 9, []string{"secret"}, 1))
	groups, err = GetUserUsableGroupsForUser(9, "default")
	require.NoError(t, err)
	require.Contains(t, groups, "secret")
}
