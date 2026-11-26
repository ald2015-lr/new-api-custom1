package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// createLog inserts a log row outside gorm's default transaction (no BEGIN
// and COMMIT around the single-row INSERT of every relay request), with
// prepared statements on as in production. The row is written, other writes
// keep the default transaction, and an INSERT the database refuses still
// returns its error.
func TestCreateLogRunsOutsideTheDefaultTransaction(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), newGormConfig(true))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	previousLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	var inTransaction []bool
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:record_transaction", func(tx *gorm.DB) {
		_, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
		inTransaction = append(inTransaction, ok)
	}))

	log := &Log{UserId: 1, Type: LogTypeConsume, Content: "relay log", ModelName: "gpt-4o"}
	require.NoError(t, createLog(log))
	assert.Equal(t, []bool{false}, inTransaction, "createLog runs outside a transaction")
	assert.NotEmpty(t, log.RequestId)
	var stored Log
	require.NoError(t, db.First(&stored, log.Id).Error)
	assert.Equal(t, "relay log", stored.Content)
	assert.Equal(t, log.RequestId, stored.RequestId)

	require.NoError(t, db.Create(&Log{UserId: 2, Type: LogTypeConsume}).Error)
	assert.Equal(t, []bool{false, true}, inTransaction, "other inserts keep the default transaction")

	assert.Error(t, createLog(&Log{Id: log.Id, UserId: 1, Type: LogTypeConsume}), "a duplicate id is refused")
	var count int64
	require.NoError(t, db.Model(&Log{}).Count(&count).Error)
	assert.EqualValues(t, 2, count)
}
