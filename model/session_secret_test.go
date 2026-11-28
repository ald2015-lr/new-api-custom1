package model

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const keptSessionSecretPattern = `^[0-9a-f]{64}$`

// useSessionSecretTestDB gives the test its own database with the options and
// server_secrets tables, starts it like InitEnv does without SESSION_SECRET
// and CRYPTO_SECRET, and restores every global it changes.
func useSessionSecretTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSessionSecret, previousCryptoSecret := common.SessionSecret, common.CryptoSecret
	previousSource, previousCryptoFromEnv := common.SessionSecretSource, common.CryptoSecretFromEnv
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&Option{}, &ServerSecret{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.SessionSecret, common.CryptoSecret = previousSessionSecret, previousCryptoSecret
		common.SessionSecretSource, common.CryptoSecretFromEnv = previousSource, previousCryptoFromEnv
		_ = sqlDB.Close()
	})
	startWithoutSessionSecretEnv("random-at-start-1")
	return db
}

// startWithoutSessionSecretEnv is what InitEnv leaves at a start without
// SESSION_SECRET and CRYPTO_SECRET: one random key for both secrets.
func startWithoutSessionSecretEnv(random string) {
	common.SessionSecret, common.CryptoSecret = random, random
	common.SessionSecretSource = common.SessionSecretSourceRandom
	common.CryptoSecretFromEnv = false
}

func saveKeepLoginOption(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	require.NoError(t, db.Save(&Option{Key: KeepLoginAcrossRestartOptionKey, Value: value}).Error)
}

func keptSessionSecrets(t *testing.T, db *gorm.DB) []ServerSecret {
	t.Helper()
	var rows []ServerSecret
	require.NoError(t, db.Find(&rows).Error)
	return rows
}

func TestInitSessionSecretKeepsTheKeyAcrossRestarts(t *testing.T) {
	for _, saved := range []string{"", "true"} {
		t.Run("saved="+saved, func(t *testing.T) {
			db := useSessionSecretTestDB(t)
			if saved != "" {
				saveKeepLoginOption(t, db, saved)
			}

			InitSessionSecret()
			kept := common.SessionSecret
			assert.Regexp(t, keptSessionSecretPattern, kept)
			assert.NotEqual(t, "random-at-start-1", kept)
			assert.Equal(t, kept, common.CryptoSecret, "CryptoSecret follows the kept key while CRYPTO_SECRET is unset")
			assert.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
			assert.True(t, common.CryptoSecretSurvivesRestart())
			// CryptoSecret is now the kept key, which a database copy holds next
			// to the audit fingerprints.
			fingerprintKey := common.AuditFingerprintSecret()
			assert.Regexp(t, keptSessionSecretPattern, fingerprintKey, "audit fingerprints use a random key of this run")
			assert.NotEqual(t, kept, fingerprintKey, "audit fingerprints are never keyed with the kept key")
			rows := keptSessionSecrets(t, db)
			require.Len(t, rows, 1)
			assert.Equal(t, sessionSecretName, rows[0].Name)
			assert.Equal(t, kept, rows[0].Value)
			assert.Positive(t, rows[0].CreatedAt)

			startWithoutSessionSecretEnv("random-at-start-2")
			InitSessionSecret()
			assert.Equal(t, kept, common.SessionSecret, "a restart must sign with the same key")
			assert.Equal(t, kept, common.CryptoSecret)
			assert.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
			assert.Equal(t, rows, keptSessionSecrets(t, db), "a restart must not write the key again")
		})
	}
}

func TestInitSessionSecretKeepsCryptoSecretFromEnv(t *testing.T) {
	db := useSessionSecretTestDB(t)
	common.CryptoSecret, common.CryptoSecretFromEnv = "crypto-from-env", true

	InitSessionSecret()
	assert.Regexp(t, keptSessionSecretPattern, common.SessionSecret)
	assert.Equal(t, "crypto-from-env", common.CryptoSecret)
	assert.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
	assert.Len(t, keptSessionSecrets(t, db), 1)
}

func TestInitSessionSecretOffKeepsTheRandomKeyAndDeletesTheKeptOne(t *testing.T) {
	db := useSessionSecretTestDB(t)
	InitSessionSecret()
	old := common.SessionSecret
	require.Len(t, keptSessionSecrets(t, db), 1)

	saveKeepLoginOption(t, db, "false")
	startWithoutSessionSecretEnv("random-at-start-2")
	InitSessionSecret()
	assert.Equal(t, "random-at-start-2", common.SessionSecret)
	assert.Equal(t, "random-at-start-2", common.CryptoSecret)
	assert.Equal(t, common.SessionSecretSourceRandom, common.SessionSecretSource)
	assert.False(t, common.CryptoSecretSurvivesRestart())
	assert.Equal(t, "random-at-start-2", common.AuditFingerprintSecret(), "a random CryptoSecret is never stored, so it keys the audit fingerprints")
	assert.Empty(t, keptSessionSecrets(t, db))

	saveKeepLoginOption(t, db, "true")
	startWithoutSessionSecretEnv("random-at-start-3")
	InitSessionSecret()
	assert.Regexp(t, keptSessionSecretPattern, common.SessionSecret)
	assert.NotEqual(t, old, common.SessionSecret, "a key deleted while the setting was off must never come back")
	assert.Equal(t, common.SessionSecretSourceDatabase, common.SessionSecretSource)
}

func TestInitSessionSecretFromEnvDeletesTheKeptKey(t *testing.T) {
	db := useSessionSecretTestDB(t)
	InitSessionSecret()
	require.Len(t, keptSessionSecrets(t, db), 1)

	common.SessionSecret, common.CryptoSecret = "from-env", "from-env"
	common.SessionSecretSource = common.SessionSecretSourceEnv
	InitSessionSecret()
	assert.Equal(t, "from-env", common.SessionSecret)
	assert.Equal(t, "from-env", common.CryptoSecret)
	assert.Equal(t, common.SessionSecretSourceEnv, common.SessionSecretSource)
	assert.True(t, common.CryptoSecretSurvivesRestart())
	assert.Equal(t, "from-env", common.AuditFingerprintSecret(), "SESSION_SECRET is never stored, so it keys the audit fingerprints")
	assert.Empty(t, keptSessionSecrets(t, db), "removing SESSION_SECRET later must not bring an old key back")
}

func TestInitSessionSecretFallsBackToTheRandomKey(t *testing.T) {
	notHex := strings.Repeat("z", 64)
	tests := []struct {
		name    string
		prepare func(t *testing.T, db *gorm.DB)
		kept    []ServerSecret
	}{
		{name: "server_secrets table missing", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Migrator().DropTable(&ServerSecret{}))
		}},
		{name: "options table missing", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Migrator().DropTable(&Option{}))
		}},
		{name: "kept key too short", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Create(&ServerSecret{Name: sessionSecretName, Value: "short", CreatedAt: 1}).Error)
		}, kept: []ServerSecret{{Name: sessionSecretName, Value: "short", CreatedAt: 1}}},
		{name: "kept key not hex", prepare: func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Create(&ServerSecret{Name: sessionSecretName, Value: notHex, CreatedAt: 1}).Error)
		}, kept: []ServerSecret{{Name: sessionSecretName, Value: notHex, CreatedAt: 1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := useSessionSecretTestDB(t)
			tc.prepare(t, db)

			require.NotPanics(t, InitSessionSecret)
			assert.Equal(t, "random-at-start-1", common.SessionSecret)
			assert.Equal(t, "random-at-start-1", common.CryptoSecret)
			assert.Equal(t, common.SessionSecretSourceRandom, common.SessionSecretSource)
			if db.Migrator().HasTable(&ServerSecret{}) {
				assert.Equal(t, tc.kept, append([]ServerSecret(nil), keptSessionSecrets(t, db)...), "an invalid kept key is never overwritten")
			}
		})
	}
}

func TestLoadOrCreateSessionSecretGivesConcurrentFirstStartsOneKey(t *testing.T) {
	// The production SQLite DSN parameters (common/database.go), on one file
	// opened by two handles like two nodes.
	dsn := filepath.Join(t.TempDir(), "keep-login.db") + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	nodes := []*gorm.DB{open(), open()}
	require.NoError(t, nodes[0].AutoMigrate(&ServerSecret{}))

	type result struct {
		secret  string
		created bool
		err     error
	}
	const starts = 8
	results := make([]result, starts)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			secret, created, err := loadOrCreateSessionSecret(nodes[i%len(nodes)])
			results[i] = result{secret: secret, created: created, err: err}
		}()
	}
	close(begin)
	wg.Wait()

	creators := 0
	for _, r := range results {
		require.NoError(t, r.err)
		assert.Regexp(t, keptSessionSecretPattern, r.secret)
		assert.Equal(t, results[0].secret, r.secret, "every start must use the same key")
		if r.created {
			creators++
		}
	}
	assert.Equal(t, 1, creators, "exactly one start makes the key")
	rows := keptSessionSecrets(t, nodes[1])
	require.Len(t, rows, 1)
	assert.Equal(t, results[0].secret, rows[0].Value)
}

// captureSessionSecretLogs collects the server log lines (SysLog, SysError).
func captureSessionSecretLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter, previousErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter, gin.DefaultErrorWriter = previousWriter, previousErrorWriter
		common.LogWriterMu.Unlock()
	})
	return &output
}

func TestInitSessionSecretNeverLogsTheKey(t *testing.T) {
	// DEBUG=true: the database logs every statement with its values.
	previousDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() { common.DebugEnabled = previousDebug })
	db := useSessionSecretTestDB(t)
	var sqlLog bytes.Buffer
	DB = db.Session(&gorm.Session{Logger: newGormLogger(&sqlLog)}).Debug()
	serverLog := captureSessionSecretLogs(t)

	InitSessionSecret()
	first := common.SessionSecret
	startWithoutSessionSecretEnv("random-at-start-2")
	InitSessionSecret()
	saveKeepLoginOption(t, db, "false")
	startWithoutSessionSecretEnv("random-at-start-3")
	InitSessionSecret()
	saveKeepLoginOption(t, db, "true")
	startWithoutSessionSecretEnv("random-at-start-4")
	InitSessionSecret()
	second := common.SessionSecret
	require.NoError(t, db.Model(&ServerSecret{}).Where("name = ?", sessionSecretName).Update("value", second[:40]).Error)
	startWithoutSessionSecretEnv("random-at-start-5")
	InitSessionSecret()
	require.Equal(t, common.SessionSecretSourceRandom, common.SessionSecretSource)

	assert.Contains(t, sqlLog.String(), KeepLoginAcrossRestartOptionKey, "the database log must be on for this test to mean anything")
	for _, line := range []string{
		"saved a new login signing key in the database",
		"using the login signing key kept in the database",
		"keep users signed in after restarts is off",
		"could not load the login signing key (the kept login signing key is invalid)",
	} {
		assert.Contains(t, serverLog.String(), line)
	}
	for _, key := range []string{first, second} {
		for _, logged := range []string{sqlLog.String(), serverLog.String()} {
			assert.NotContains(t, logged, key[:16])
		}
	}
}
