package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReplaceUserGroupGrantsIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&UserGroupGrant{}))
	require.NoError(t, ReplaceUserGroupGrants(db, 7, []string{"secret", "secret", ""}, 3))
	var grants []UserGroupGrant
	require.NoError(t, db.Find(&grants).Error)
	require.Len(t, grants, 1)
	require.Equal(t, "secret", grants[0].GroupName)
	require.NoError(t, ReplaceUserGroupGrants(db, 7, []string{"other"}, 3))
	require.NoError(t, db.Find(&grants).Error)
	require.Len(t, grants, 1)
	require.Equal(t, "other", grants[0].GroupName)
}
