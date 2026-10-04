package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestInitDBMigratesTaskPluginsAndAuditLogs(t *testing.T) {
	previousDB, previousLogDB := DB, LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
	previousRedis := common.RedisEnabled
	databasePath := filepath.Join(t.TempDir(), "migration.db")
	t.Setenv("SQL_DSN", "local")
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.SQLitePath = true, databasePath
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	require.NoError(t, InitDB())
	require.NoError(t, InitLogDB())
	db := DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite
		common.RedisEnabled = previousRedis
	})

	require.True(t, db.Migrator().HasTable(&TaskPlugin{}))
	require.True(t, db.Migrator().HasTable(&AuditLog{}))
}
