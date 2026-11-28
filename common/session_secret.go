package common

import (
	"crypto/rand"
	"encoding/hex"
)

// SecretSource says where this run's SessionSecret, the key that signs
// logins, came from.
type SecretSource string

const (
	// SessionSecretSourceEnv: the SESSION_SECRET environment variable.
	SessionSecretSourceEnv SecretSource = "env"
	// SessionSecretSourceDatabase: the key kept in the server_secrets table
	// while "keep users signed in after restarts" is on.
	SessionSecretSourceDatabase SecretSource = "database"
	// SessionSecretSourceRandom: made at this start, so a restart signs
	// everyone out.
	SessionSecretSourceRandom SecretSource = "random"
)

// SessionSecretSource and CryptoSecretFromEnv are set once at startup, by
// InitEnv and model.InitSessionSecret, before anything reads the secrets.
var SessionSecretSource = SessionSecretSourceRandom

// CryptoSecretFromEnv: CryptoSecret is the CRYPTO_SECRET variable.
var CryptoSecretFromEnv bool

// UsePersistedSessionSecret makes the key kept in the database this run's
// SessionSecret, and CryptoSecret too unless CRYPTO_SECRET is set (the rule
// InitEnv applies to SESSION_SECRET).
func UsePersistedSessionSecret(secret string) {
	SessionSecret = secret
	if !CryptoSecretFromEnv {
		CryptoSecret = secret
	}
	SessionSecretSource = SessionSecretSourceDatabase
}

// CryptoSecretSurvivesRestart reports whether CryptoSecret stays the same
// after a restart: CRYPTO_SECRET is set, or SessionSecret is not random.
func CryptoSecretSurvivesRestart() bool {
	return CryptoSecretFromEnv || SessionSecretSource != SessionSecretSourceRandom
}

// auditFingerprintRunSecret is made at this start and never stored; see
// AuditFingerprintSecret.
var auditFingerprintRunSecret = func() string {
	random := make([]byte, 32)
	_, _ = rand.Read(random) // never fails since Go 1.24
	return hex.EncodeToString(random)
}()

// AuditFingerprintSecret is the key of the audit "input fingerprints": a
// login or password reset input that is neither an account name nor an email
// address is stored only as a fingerprint, since it is often a password typed
// into the wrong box. It is CryptoSecret, unless that is the key kept in the
// database: the fingerprints are in the same database (audit_logs), so a copy
// or backup of it would let its holder test guesses offline. Then it is a key
// made at this start and never stored, and fingerprints match within one run
// only, as they did before the key was kept.
func AuditFingerprintSecret() string {
	if SessionSecretSource == SessionSecretSourceDatabase && !CryptoSecretFromEnv {
		return auditFingerprintRunSecret
	}
	return CryptoSecret
}
