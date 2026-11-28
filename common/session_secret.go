package common

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
