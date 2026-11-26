package perfmetrics

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// useRedisSwitch sets common.RedisEnabled and common.RDB for one test.
func useRedisSwitch(t *testing.T, enabled bool, client *redis.Client) {
	t.Helper()
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = enabled, client
	t.Cleanup(func() { common.RedisEnabled, common.RDB = previousEnabled, previousClient })
}

// Tests switch common.RedisEnabled and common.RDB in their setup and cleanup,
// right after requests that recorded a sample. Record reads both before it
// returns, so its background work never reads them while a test changes them
// (go test -race reported that read in the controller package).
func TestRecordReadsTheRedisSwitchBeforeReturning(t *testing.T) {
	useRedisSwitch(t, false, nil)

	Record(Sample{Model: "redis-switch-model", Group: "default", Success: true, LatencyMs: 1})
	// What the next test's setup does.
	common.RedisEnabled, common.RDB = false, nil
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
}

// With Redis on, the sample still reaches the client Record saw, even when
// the switch changes before the background write runs.
func TestRecordSendsTheSampleToTheRedisItSaw(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	useRedisSwitch(t, true, client)

	Record(Sample{Model: "redis-on-model", Group: "default", Success: true, LatencyMs: 7})
	common.RedisEnabled, common.RDB = false, nil

	require.Eventually(t, func() bool {
		for _, key := range server.Keys() {
			if strings.HasPrefix(key, "perf:redis-on-model:default:") {
				return server.HGet(key, "req") == "1" && server.HGet(key, "lat") == "7"
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
}
