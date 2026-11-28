package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The audit input fingerprints are keyed with CryptoSecret unless that is the
// key kept in the database, which a copy of the database would reveal next to
// the fingerprints.
func TestAuditFingerprintSecretIsNeverTheKeptKey(t *testing.T) {
	previousSessionSecret, previousCryptoSecret := SessionSecret, CryptoSecret
	previousSource, previousCryptoFromEnv := SessionSecretSource, CryptoSecretFromEnv
	t.Cleanup(func() {
		SessionSecret, CryptoSecret = previousSessionSecret, previousCryptoSecret
		SessionSecretSource, CryptoSecretFromEnv = previousSource, previousCryptoFromEnv
	})
	// What InitEnv leaves without SESSION_SECRET and CRYPTO_SECRET.
	startRandom := func() {
		SessionSecret, CryptoSecret = "random-at-start", "random-at-start"
		SessionSecretSource, CryptoSecretFromEnv = SessionSecretSourceRandom, false
	}

	startRandom()
	assert.Equal(t, "random-at-start", AuditFingerprintSecret(), "a random CryptoSecret is never stored")

	SessionSecret, CryptoSecret, SessionSecretSource = "from-env", "from-env", SessionSecretSourceEnv
	assert.Equal(t, "from-env", AuditFingerprintSecret(), "SESSION_SECRET is never stored")

	startRandom()
	CryptoSecret, CryptoSecretFromEnv = "crypto-from-env", true
	UsePersistedSessionSecret("kept-in-the-database")
	assert.Equal(t, "crypto-from-env", AuditFingerprintSecret(), "CRYPTO_SECRET is never stored")

	startRandom()
	UsePersistedSessionSecret("kept-in-the-database")
	require.Equal(t, "kept-in-the-database", CryptoSecret)
	secret := AuditFingerprintSecret()
	assert.Regexp(t, `^[0-9a-f]{64}$`, secret)
	assert.NotEqual(t, "kept-in-the-database", secret)
	assert.Equal(t, secret, AuditFingerprintSecret(), "the same key for the whole run")
}
