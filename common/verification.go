package common

import (
	"crypto/subtle"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type verificationValue struct {
	code     string
	time     time.Time
	failures int
}

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
)

// verificationMaxFailures is the number of wrong guesses that deletes a stored
// registration code, after which a new one must be requested.
const verificationMaxFailures = 5

var verificationMutex sync.Mutex
var verificationMap map[string]verificationValue
var verificationMapMaxSize = 10
var VerificationValidMinutes = 10

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

func RegisterVerificationCodeWithKey(key string, code string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap[purpose+key] = verificationValue{
		code: code,
		time: time.Now(),
	}
	if len(verificationMap) > verificationMapMaxSize {
		removeExpiredPairs()
	}
}

// VerifyCodeWithKey reports whether code is the unexpired code stored for
// key. A correct guess leaves the code as it is; callers delete a code once it
// has served its purpose.
//
// Wrong guesses at a registration code are counted, and the
// verificationMaxFailures-th one deletes the code: it has only 6 hexadecimal
// characters. Password reset tokens have 32 random hexadecimal characters, so
// counting guesses at them would only let anyone who knows an address cancel
// its reset link.
func VerifyCodeWithKey(key string, code string, purpose string) bool {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	mapKey := purpose + key
	value, okay := verificationMap[mapKey]
	now := time.Now()
	if !okay || int(now.Sub(value.time).Seconds()) >= VerificationValidMinutes*60 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(value.code)) == 1 {
		return true
	}
	if purpose != EmailVerificationPurpose {
		return false
	}
	value.failures++
	if value.failures >= verificationMaxFailures {
		delete(verificationMap, mapKey)
		return false
	}
	verificationMap[mapKey] = value
	return false
}

func DeleteKey(key string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	delete(verificationMap, purpose+key)
}

// no lock inside, so the caller must lock the verificationMap before calling!
func removeExpiredPairs() {
	now := time.Now()
	for key := range verificationMap {
		if int(now.Sub(verificationMap[key].time).Seconds()) >= VerificationValidMinutes*60 {
			delete(verificationMap, key)
		}
	}
}

func init() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
}
