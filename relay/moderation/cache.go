package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
)

const (
	verdictCacheNamespace = "new-api:moderation_verdict:v1"
	verdictTTL            = time.Hour
	verdictMemoryEntries  = 100_000
	frontCacheEntries     = 20_000
	frontCacheTTL         = 10 * time.Minute
	cacheWriteTimeout     = 2 * time.Second
)

// Verdict is OpenAI's answer for one text piece or image. It does not depend
// on the key that asked, so every channel with the same moderation endpoint
// reuses it; the channel's category filter is applied when deciding.
type Verdict struct {
	Flagged           bool                `json:"flagged"`
	Categories        []string            `json:"categories,omitempty"`          // the true ones
	Scores            map[string]float64  `json:"scores,omitempty"`              // top 5, 4 decimals
	AppliedInputTypes map[string][]string `json:"applied_input_types,omitempty"` // true categories only
	Model             string              `json:"model,omitempty"`
}

// verdictStore is the process-wide verdict cache: Redis when it is on
// (behind a process-local front cache), memory otherwise. Failures are never
// stored.
type verdictStore struct {
	shared       *cachex.HybridCache[Verdict]
	front        *hot.HotCache[string, Verdict]
	redisEnabled func() bool
}

func newVerdictStore(client *redis.Client, redisEnabled func() bool) *verdictStore {
	return &verdictStore{
		shared: cachex.NewHybridCache[Verdict](cachex.HybridCacheConfig[Verdict]{
			Namespace:    verdictCacheNamespace,
			Redis:        client,
			RedisCodec:   cachex.JSONCodec[Verdict]{},
			RedisEnabled: redisEnabled,
			Memory: func() *hot.HotCache[string, Verdict] {
				return hot.NewHotCache[string, Verdict](hot.LRU, verdictMemoryEntries).WithTTL(verdictTTL).WithJanitor().Build()
			},
		}),
		front:        hot.NewHotCache[string, Verdict](hot.LRU, frontCacheEntries).WithTTL(frontCacheTTL).WithJanitor().Build(),
		redisEnabled: func() bool { return client != nil && redisEnabled() },
	}
}

var (
	storeOnce sync.Once
	store     *verdictStore
)

func verdicts() *verdictStore {
	storeOnce.Do(func() {
		store = newVerdictStore(common.RDB, func() bool { return common.RedisEnabled && common.RDB != nil })
	})
	return store
}

// lookup returns the verdicts of keys it holds: the front cache first when
// Redis is on, then one batched read on ctx. A failed read counts every key
// it asked for as a miss.
func (s *verdictStore) lookup(ctx context.Context, keys []string) map[string]Verdict {
	found := make(map[string]Verdict, len(keys))
	if len(keys) == 0 {
		return found
	}
	redisOn := s.redisEnabled()
	missing := keys
	if redisOn {
		missing = make([]string, 0, len(keys))
		for _, key := range keys {
			if verdict, ok, _ := s.front.Get(key); ok {
				found[key] = verdict
				continue
			}
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return found
	}
	values, err := s.shared.GetMany(ctx, missing)
	if err != nil {
		logger.LogDebug(ctx, "content moderation verdict cache read failed: %s", err.Error())
		return found
	}
	for key, verdict := range values {
		found[key] = verdict
		if redisOn {
			s.front.SetWithTTL(key, verdict, frontCacheTTL)
		}
	}
	return found
}

// save stores fresh verdicts without delaying the request: the front cache
// now, the shared cache in one pipelined write in the background.
func (s *verdictStore) save(values map[string]Verdict) {
	if len(values) == 0 {
		return
	}
	if s.redisEnabled() {
		for key, verdict := range values {
			s.front.SetWithTTL(key, verdict, frontCacheTTL)
		}
	}
	gopool.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
		defer cancel()
		if err := s.shared.SetManyWithTTL(ctx, values, verdictTTL); err != nil {
			logger.LogDebug(ctx, "content moderation verdict cache write failed: %s", err.Error())
		}
	})
}

// textKey and imageKey identify a verdict by content and moderation endpoint,
// so a reseller or stub endpoint cannot plant verdicts the official one then
// trusts. They hold hashes only, never text or URLs.
func textKey(endpoint, piece string) string {
	return verdictKey(endpoint, "text", piece)
}

func imageKey(endpoint, url string) string {
	return verdictKey(endpoint, "image", url)
}

func verdictKey(endpoint, kind, content string) string {
	sum := sha256.Sum256([]byte(dto.ModerationModel + "\x00" + endpoint + "\x00" + kind + "\x00" + content))
	return hex.EncodeToString(sum[:])
}
