package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	// KeepLoginAcrossRestartOptionKey is the root setting "keep users signed
	// in after restarts": "true" (the default) or "false". The server reads it
	// at start only, so a change takes effect at the next restart.
	KeepLoginAcrossRestartOptionKey = "KeepLoginAcrossRestartEnabled"
	// KeepLoginAcrossRestartSourceOptionKey is the read-only value GET
	// /api/option adds: where this run's login signing key came from
	// (common.SecretSource). It is never stored.
	KeepLoginAcrossRestartSourceOptionKey = "KeepLoginAcrossRestartSource"

	sessionSecretName = "session_secret"
	// 32 random bytes, hex encoded.
	sessionSecretLength = 64
)

var (
	errInvalidKeptSessionSecret = errors.New("the kept login signing key is invalid")
	errKeptSessionSecretMissing = errors.New("the kept login signing key disappeared while it was created")
)

// ServerSecret is a secret the server makes for itself, by name. It has a
// table of its own: options are loaded into OptionMap, returned by
// GET /api/option, synchronised between nodes and audited.
type ServerSecret struct {
	Name      string `gorm:"type:varchar(64);primaryKey"`
	Value     string `gorm:"type:varchar(128);not null"`
	CreatedAt int64  `gorm:"type:bigint;autoCreateTime"`
}

func (ServerSecret) TableName() string { return "server_secrets" }

// InitSessionSecret decides this run's login signing key. It runs once at
// start, after the options are loaded and before anything signs or caches
// with common.SessionSecret or common.CryptoSecret:
//   - SESSION_SECRET set: that key stays (InitEnv) and the kept key is
//     deleted, so removing SESSION_SECRET later cannot bring old logins back.
//   - "keep users signed in after restarts" off: the random key of this start
//     stays and the kept key is deleted, so turning the setting on later
//     starts from a new key.
//   - on (the default): the key kept in server_secrets, made by the first
//     start; CryptoSecret follows it unless CRYPTO_SECRET is set.
//
// A database error keeps the random key, as before this setting existed, and
// is logged; the start goes on. The key itself is never logged.
func InitSessionSecret() {
	if common.SessionSecretSource == common.SessionSecretSourceEnv {
		common.SysLog(common.LogText("SESSION_SECRET is set: it signs logins, and the keep users signed in after restarts setting has no effect"))
		deleteKeptSessionSecret(DB)
		return
	}
	// The row itself, not OptionMap: InitOptionMap keeps the default "true"
	// when it cannot read the options, and a failed read must not count as on.
	var options []Option
	if err := DB.Where(&Option{Key: KeepLoginAcrossRestartOptionKey}).Limit(1).Find(&options).Error; err != nil {
		logKeptSessionSecretLoadError(err)
		return
	}
	if len(options) == 1 && options[0].Value != "true" {
		common.SysLog(common.LogText("keep users signed in after restarts is off: this start uses a random login signing key, so users signed in before must sign in again"))
		deleteKeptSessionSecret(DB)
		return
	}
	secret, created, err := loadOrCreateSessionSecret(DB)
	if err != nil {
		logKeptSessionSecretLoadError(err)
		return
	}
	common.UsePersistedSessionSecret(secret)
	if created {
		common.SysLog(common.LogText("keep users signed in after restarts: saved a new login signing key in the database; users signed in before this start must sign in again"))
	} else {
		common.SysLog(common.LogText("keep users signed in after restarts: using the login signing key kept in the database"))
	}
}

func logKeptSessionSecretLoadError(err error) {
	common.SysError(common.LogText("keep users signed in after restarts: could not load the login signing key (%s); this start uses a random key, so every user must sign in again now and after the next restart", sanitizeDBError(err).Error()))
}

// quietSecretDB runs the statements on server_secrets without the database
// log: with DEBUG=true it prints every statement with its values.
func quietSecretDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{Logger: logger.Discard})
}

// loadOrCreateSessionSecret returns the kept login signing key, making it
// when there is none. Nodes that start at the same time on one database all
// get the same key: each inserts its own only if no row exists, then reads
// back the one that was stored. created says this call stored it.
func loadOrCreateSessionSecret(db *gorm.DB) (secret string, created bool, err error) {
	quiet := quietSecretDB(db)
	secret, found, err := readKeptSessionSecret(quiet)
	if err != nil {
		return "", false, err
	}
	if !found {
		random := make([]byte, sessionSecretLength/2)
		_, _ = rand.Read(random) // never fails since Go 1.24
		candidate := hex.EncodeToString(random)
		if err := quiet.Clauses(clause.OnConflict{DoNothing: true}).Create(&ServerSecret{Name: sessionSecretName, Value: candidate}).Error; err != nil {
			return "", false, err
		}
		if secret, found, err = readKeptSessionSecret(quiet); err != nil {
			return "", false, err
		}
		if !found {
			return "", false, errKeptSessionSecretMissing
		}
		created = secret == candidate
	}
	// An edited row is never overwritten: DEPLOY.md says how to replace it.
	if _, decodeErr := hex.DecodeString(secret); len(secret) != sessionSecretLength || decodeErr != nil {
		return "", false, errInvalidKeptSessionSecret
	}
	return secret, created, nil
}

func readKeptSessionSecret(quiet *gorm.DB) (string, bool, error) {
	var rows []ServerSecret
	if err := quiet.Where("name = ?", sessionSecretName).Limit(1).Find(&rows).Error; err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Value, true, nil
}

// deleteKeptSessionSecret deletes the kept key at a start that does not use
// it. A failure is logged and the start goes on; the next start tries again.
func deleteKeptSessionSecret(db *gorm.DB) {
	if err := quietSecretDB(db).Where("name = ?", sessionSecretName).Delete(&ServerSecret{}).Error; err != nil {
		common.SysError(common.LogText("keep users signed in after restarts: could not delete the login signing key kept in the database (%s); the next start tries again, and turning the setting back on before then reuses the old key", sanitizeDBError(err).Error()))
	}
}
