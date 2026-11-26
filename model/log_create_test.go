package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	chgoproto "github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2"
	chcolumn "github.com/ClickHouse/clickhouse-go/v2/lib/column"
	chproto "github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
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

// fakeClickHouseHTTP is a ClickHouse HTTP endpoint that answers what opening
// a log database and inserting logs ask for (the server hello, the version,
// DESCRIBE TABLE with the columns of Log) and counts the INSERTs it receives.
func fakeClickHouseHTTP(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	logSchema, err := schema.Parse(&Log{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	var describe [][]any
	for _, field := range logSchema.Fields {
		if field.DBName == "" {
			continue
		}
		columnType := "String"
		switch field.FieldType.Kind() {
		case reflect.Int64:
			columnType = "Int64"
		case reflect.Int, reflect.Int32:
			columnType = "Int32"
		case reflect.Bool:
			columnType = "UInt8"
		}
		describe = append(describe, []any{field.DBName, columnType, "", "", "", "", ""})
	}
	reply := func(w http.ResponseWriter, columns []string, rows ...[]any) {
		block := chproto.NewBlock()
		for _, column := range columns {
			name, columnType, _ := strings.Cut(column, " ")
			assert.NoError(t, block.AddColumn(name, chcolumn.Type(columnType)))
		}
		for _, row := range rows {
			assert.NoError(t, block.Append(row...))
		}
		var buffer chgoproto.Buffer
		assert.NoError(t, block.Encode(&buffer, clickhouse.ClientTCPProtocolVersion))
		_, _ = w.Write(buffer.Buf)
	}
	var inserts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		query := r.URL.Query().Get("query")
		if query == "" {
			query = string(body)
		}
		query = strings.TrimSpace(query)
		switch {
		case strings.HasPrefix(query, "SELECT displayName()"):
			reply(w, []string{"displayName() String", "version() String", "revision() UInt32", "timezone() String"},
				[]any{"fake", "24.8.1.1", uint32(clickhouse.ClientTCPProtocolVersion), "UTC"})
		case strings.HasPrefix(query, "SELECT version()"):
			reply(w, []string{"version() String"}, []any{"24.8.1.1"})
		case query == "SELECT 1":
			reply(w, []string{"1 UInt8"}, []any{uint8(1)})
		case strings.HasPrefix(query, "DESCRIBE TABLE"):
			reply(w, []string{"name String", "type String", "default_type String", "default_expression String",
				"comment String", "codec_expression String", "ttl_expression String"}, describe...)
		case strings.HasPrefix(strings.ToUpper(query), "INSERT"):
			inserts.Add(1)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, &inserts
}

// On a ClickHouse log database, createLog sends every row. gorm's ClickHouse
// dialect inserts through a prepared batch that only the commit of gorm's
// default transaction sends; outside it the row was dropped while Create
// returned no error.
func TestCreateLogSendsEveryRowToClickHouse(t *testing.T) {
	url, inserts := fakeClickHouseHTTP(t)
	t.Setenv("LOG_SQL_DSN", url+"/default")
	db, dbType, err := chooseDB("LOG_SQL_DSN", true)
	require.NoError(t, err)
	require.Equal(t, common.DatabaseTypeClickHouse, dbType)
	previousLogDB, previousLogType := LOG_DB, common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(dbType)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	for range 3 {
		require.NoError(t, createLog(&Log{UserId: 1, Type: LogTypeConsume, Content: "relay log", ModelName: "gpt-4o"}))
	}
	RecordLog(1, LogTypeSystem, common.NewMessage("system log"))

	assert.EqualValues(t, 4, inserts.Load(), "one INSERT per log row")
}
