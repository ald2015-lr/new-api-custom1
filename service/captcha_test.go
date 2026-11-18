package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useMemoryCaptchaStore swaps in an empty in-memory store whose clock the
// returned pointer controls, with Redis disabled.
func useMemoryCaptchaStore(t *testing.T, limit int) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	previousStore, previousRedisEnabled := memoryCaptchas, common.RedisEnabled
	memoryCaptchas = newMemoryCaptchaStore(limit, func() time.Time { return now })
	common.RedisEnabled = false
	t.Cleanup(func() { memoryCaptchas, common.RedisEnabled = previousStore, previousRedisEnabled })
	return &now
}

// useRedisCaptchaStore points the captcha store at a fresh miniredis.
func useRedisCaptchaStore(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	previousClient, previousRedisEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB, common.RedisEnabled = previousClient, previousRedisEnabled
	})
	return server
}

// captchaStoreFixtures run a test against both stores. use returns a function
// that moves the store past CaptchaTTL.
var captchaStoreFixtures = []struct {
	name string
	use  func(t *testing.T) (expire func())
}{
	{"memory", func(t *testing.T) func() {
		now := useMemoryCaptchaStore(t, captchaMemoryMaxEntries)
		return func() { *now = now.Add(CaptchaTTL) }
	}},
	{"redis", func(t *testing.T) func() {
		server := useRedisCaptchaStore(t)
		return func() { server.FastForward(CaptchaTTL) }
	}},
}

func TestCaptchaChallengeHasWholeAnswersInRange(t *testing.T) {
	pattern := regexp.MustCompile(`^([0-9]+)([-+×÷])([0-9]+)=\?$`)
	operators := map[string]int{}
	for seed := range uint64(2000) {
		challenge := newCaptchaChallenge(rand.New(rand.NewPCG(seed, 7)))
		match := pattern.FindStringSubmatch(challenge.Text)
		require.NotNil(t, match, challenge.Text)
		a, err := strconv.Atoi(match[1])
		require.NoError(t, err)
		b, err := strconv.Atoi(match[3])
		require.NoError(t, err)
		operators[match[2]]++
		switch match[2] {
		case "+":
			assert.True(t, a >= 1 && a <= 20 && b >= 1 && b <= 20, challenge.Text)
			assert.Equal(t, a+b, challenge.Answer, challenge.Text)
		case "-":
			assert.True(t, a >= 2 && a <= 20 && b >= 1 && b <= a, challenge.Text)
			assert.Equal(t, a-b, challenge.Answer, challenge.Text)
		case "×":
			assert.True(t, a >= 2 && a <= 9 && b >= 2 && b <= 9, challenge.Text)
			assert.Equal(t, a*b, challenge.Answer, challenge.Text)
		case "÷":
			assert.True(t, b >= 2 && b <= 9 && a%b == 0, challenge.Text)
			assert.Equal(t, a/b, challenge.Answer, challenge.Text)
			assert.True(t, challenge.Answer >= 1 && challenge.Answer <= 9, challenge.Text)
		}
		assert.True(t, challenge.Answer >= 0 && challenge.Answer <= 81, challenge.Text)
	}
	assert.Len(t, operators, 4, "every operator is drawn")
}

func TestNewCaptchaReturnsPNGAndKeepsTheAnswer(t *testing.T) {
	useMemoryCaptchaStore(t, captchaMemoryMaxEntries)
	previousRand := newCaptchaRand
	newCaptchaRand = func() *rand.Rand { return rand.New(rand.NewPCG(11, 13)) }
	t.Cleanup(func() { newCaptchaRand = previousRand })

	first, err := NewCaptcha()
	require.NoError(t, err)
	second, err := NewCaptcha()
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{32}$`, first.ID)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, 300, first.ExpiresIn)
	assert.Equal(t, first.Image, second.Image, "the same random source draws the same image")

	encoded, ok := strings.CutPrefix(first.Image, "data:image/png;base64,")
	require.True(t, ok)
	data, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	picture, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 240, 80), picture.Bounds())

	answer := strconv.Itoa(newCaptchaChallenge(rand.New(rand.NewPCG(11, 13))).Answer)
	assert.True(t, VerifyCaptcha(first.ID, answer))
	assert.False(t, VerifyCaptcha(first.ID, answer), "an answered captcha is used up")
	assert.True(t, VerifyCaptcha(second.ID, answer))
}

func TestVerifyCaptchaAllowsOneGuess(t *testing.T) {
	for _, store := range captchaStoreFixtures {
		t.Run(store.name, func(t *testing.T) {
			expire := store.use(t)

			id := IssueCaptchaForTest(16)
			assert.True(t, VerifyCaptcha(id, "16"))
			assert.False(t, VerifyCaptcha(id, "16"), "a solved captcha cannot be replayed")

			id = IssueCaptchaForTest(16)
			assert.False(t, VerifyCaptcha(id, "15"))
			assert.False(t, VerifyCaptcha(id, "16"), "a wrong guess uses the captcha up")

			for _, answer := range []string{" 16 ", "１６", "　16\t", "016"} {
				assert.True(t, VerifyCaptcha(IssueCaptchaForTest(16), answer), "%q", answer)
			}
			for _, tc := range []struct {
				answer string
				value  int
			}{
				{"", 16}, {"1e1", 10}, {"-0", 0}, {"+16", 16}, {"16.0", 16}, {"0x10", 16},
				{"1 6", 16}, {"16a", 16}, {"１６a", 16}, {"十六", 16}, {"1234", 1234},
			} {
				id := IssueCaptchaForTest(tc.value)
				assert.False(t, VerifyCaptcha(id, tc.answer), "%q", tc.answer)
				assert.False(t, VerifyCaptcha(id, strconv.Itoa(tc.value)), "a malformed answer %q uses the captcha up", tc.answer)
			}

			assert.False(t, VerifyCaptcha(common.GetUUID(), "16"), "unknown id")
			assert.False(t, VerifyCaptcha("", "16"))
			assert.False(t, VerifyCaptcha("../"+id, "16"))

			id = IssueCaptchaForTest(16)
			expire()
			assert.False(t, VerifyCaptcha(id, "16"), "an expired captcha is refused")
		})
	}
}

func TestVerifyCaptchaHasOneWinnerUnderConcurrency(t *testing.T) {
	for _, store := range captchaStoreFixtures {
		t.Run(store.name, func(t *testing.T) {
			store.use(t)
			id := IssueCaptchaForTest(42)
			var winners atomic.Int32
			var group sync.WaitGroup
			for range 32 {
				group.Go(func() {
					if VerifyCaptcha(id, "42") {
						winners.Add(1)
					}
				})
			}
			group.Wait()
			assert.EqualValues(t, 1, winners.Load())
		})
	}
}

func TestMemoryCaptchaStoreDropsExpiredAndOldestEntries(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	store := newMemoryCaptchaStore(3, func() time.Time { return now })
	take := func(id string) bool {
		_, found, err := store.take(id)
		require.NoError(t, err)
		return found
	}

	require.NoError(t, store.put("first", 1))
	now = now.Add(CaptchaTTL / 2)
	require.NoError(t, store.put("second", 2))
	now = now.Add(CaptchaTTL / 2)
	require.NoError(t, store.put("third", 3))
	assert.Len(t, store.entries, 2, "the expired entry is swept, the live one stays")

	require.NoError(t, store.put("fourth", 4))
	require.NoError(t, store.put("fifth", 5))
	assert.Len(t, store.entries, 3, "the store never holds more than its cap")
	assert.False(t, take("second"), "the oldest entry makes room")
	assert.True(t, take("third"))
	assert.True(t, take("fourth"))
	assert.True(t, take("fifth"))
	assert.Empty(t, store.entries)
}

func TestRedisCaptchaStoreSharesAnswersAndFailsClosed(t *testing.T) {
	server := useRedisCaptchaStore(t)

	captcha, err := NewCaptcha()
	require.NoError(t, err)
	key := "new-api:captcha:v1:" + captcha.ID
	assert.True(t, server.Exists(key))
	assert.Equal(t, CaptchaTTL, server.TTL(key))
	assert.False(t, VerifyCaptcha(captcha.ID, "999"))
	assert.False(t, server.Exists(key), "a checked captcha is deleted")

	id := IssueCaptchaForTest(7)
	server.Close()
	assert.False(t, VerifyCaptcha(id, "7"), "a Redis failure refuses the captcha")
	_, err = NewCaptcha()
	assert.Error(t, err)
}
