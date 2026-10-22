package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRedisRateLimitUsesUTCRegardlessOfLocalTimezone(t *testing.T) {
	redisServer, redisClient := useRateLimitMiniRedis(t)
	previousLocation := time.Local
	time.Local = time.FixedZone("test-utc-plus-eight", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocation })

	ctx := context.Background()
	recordKey := "rateLimit:model-utc-record"
	recordRedisRequest(ctx, redisClient, recordKey, 2, 60)
	recorded, err := redisClient.LIndex(ctx, recordKey, 0).Result()
	require.NoError(t, err)
	recordedAt, err := time.Parse(modelRateLimitTimeFormat, recorded)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().UTC(), recordedAt, 2*time.Second)

	checkKey := "rateLimit:model-utc-check"
	withinWindow := time.Now().UTC().Add(-30 * time.Second).Format(modelRateLimitTimeFormat)
	_, err = redisServer.Push(checkKey, withinWindow, withinWindow)
	require.NoError(t, err)
	allowed, err := checkRedisRateLimit(ctx, redisClient, checkKey, 2, 60)
	require.NoError(t, err)
	assert.False(t, allowed, "an existing UTC timestamp inside the window must remain limited on a non-UTC host")
}

var modelRateLimitTestUsers atomic.Int64

func TestModelRateLimitStreamFailuresDoNotConsumeSuccessLimit(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, backend := range []string{"memory", "redis"} {
		for _, totalLimit := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/total=%d", backend, totalLimit), func(t *testing.T) {
				subject := strconv.Itoa(7200000 + int(modelRateLimitTestUsers.Add(1)))
				limit := modelRateLimit{subject: subject, durationMinutes: 1, totalMaxCount: totalLimit, successMaxCount: 1}
				handler := memoryRateLimitHandler(&inMemoryRateLimiter, limit)
				if backend == "redis" {
					useRateLimitMiniRedis(t)
					handler = redisRateLimitHandler(limit)
				}
				router := gin.New()
				router.GET("/:outcome", handler, func(c *gin.Context) {
					status := relaycommon.NewStreamStatus()
					if c.Param("outcome") == "failed" {
						status.MarkFailed("server_error", "", 0)
					} else {
						status.MarkCompleted()
					}
					common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, status)
					c.Status(http.StatusOK)
				})
				assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/failed", "127.0.0.1:1000").Code)
				if totalLimit > 0 {
					assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/failed", "127.0.0.1:1000").Code)
				} else {
					assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/completed", "127.0.0.1:1000").Code)
				}
				assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/completed", "127.0.0.1:1000").Code)
			})
		}
	}
}

func TestModelMemoryRateLimitReservesConcurrentSuccessAdmission(t *testing.T) {
	require.NoError(t, i18n.Init())
	subject := strconv.Itoa(7200000 + int(modelRateLimitTestUsers.Add(1)))
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router := gin.New()
	router.GET("/:outcome", memoryRateLimitHandler(&inMemoryRateLimiter, modelRateLimit{subject: subject, durationMinutes: 1, successMaxCount: 1}), func(c *gin.Context) {
		if c.Param("outcome") == "failed" {
			close(entered)
			<-release
			status := relaycommon.NewStreamStatus()
			status.MarkFailed("server_error", "", 0)
			common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, status)
		}
		c.Status(http.StatusOK)
	})
	go func() {
		defer close(finished)
		assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/failed", "127.0.0.1:1000").Code)
	}()
	<-entered
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/completed", "127.0.0.1:1000").Code)
	close(release)
	<-finished
	assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/completed", "127.0.0.1:1000").Code)
	assert.Equal(t, http.StatusTooManyRequests, performRateLimitRequest(router, "/completed", "127.0.0.1:1000").Code)
}

// The memory limiter answers like the Redis limiter: an OpenAI error body
// naming the limit that was reached, not an empty 429.
func TestModelMemoryRateLimitRejectionsReturnOpenAIError(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name                     string
		totalLimit, successLimit int
		messageKey               string
	}{
		{name: "total", totalLimit: 1, messageKey: i18n.MsgRateLimitTotalReached},
		{name: "success", successLimit: 1, messageKey: i18n.MsgRateLimitReached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := strconv.Itoa(7200000 + int(modelRateLimitTestUsers.Add(1)))
			router := gin.New()
			router.GET("/", func(c *gin.Context) {
				c.Set(common.RequestIdKey, "req-limited")
			}, memoryRateLimitHandler(&inMemoryRateLimiter, modelRateLimit{subject: subject, durationMinutes: 1, totalMaxCount: tc.totalLimit, successMaxCount: tc.successLimit}), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})
			require.Equal(t, http.StatusOK, performRateLimitRequest(router, "/", "127.0.0.1:1000").Code)

			resp := performRateLimitRequest(router, "/", "127.0.0.1:1000")
			assert.Equal(t, http.StatusTooManyRequests, resp.Code)
			message := i18n.Translate("", tc.messageKey, map[string]any{"Minutes": 1, "Max": 1})
			want, err := common.Marshal(gin.H{"error": gin.H{
				"message": common.MessageWithRequestId(message, "req-limited"),
				"type":    "new_api_error",
				"code":    "",
			}})
			require.NoError(t, err)
			assert.JSONEq(t, string(want), resp.Body.String())
		})
	}
}

// A group with its own limit counts each user's requests on its own; groups
// without one keep sharing the user's global limit.
func TestModelRateLimitCountsConfiguredGroupsSeparately(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousEnabled, previousMinutes := setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes
	previousTotal, previousSuccess := setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount
	previousGroups, previousRedisEnabled := setting.ModelRequestRateLimitGroup, common.RedisEnabled
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = previousEnabled, previousMinutes
		setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = previousTotal, previousSuccess
		setting.ModelRequestRateLimitGroup, common.RedisEnabled = previousGroups, previousRedisEnabled
	})
	setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = true, 1
	common.RedisEnabled = false

	// Groups a and b have their own limits; c and d have none and share the global one.
	steps := []struct {
		group string
		want  int
	}{
		{"a", http.StatusOK}, {"a", http.StatusTooManyRequests},
		{"b", http.StatusOK}, {"b", http.StatusTooManyRequests},
		{"c", http.StatusOK}, {"d", http.StatusTooManyRequests},
	}
	// The Redis token bucket binds to the first Redis client it sees and another
	// test already drives it, so the total limit runs on the memory backend only.
	for _, tc := range []struct {
		backend, limit string
		counts         [2]int // [total, success] for groups a and b and for the global limit
	}{
		{"memory", "success", [2]int{0, 1}},
		{"redis", "success", [2]int{0, 1}},
		{"memory", "total", [2]int{1, 1000}},
	} {
		t.Run(tc.backend+"/"+tc.limit, func(t *testing.T) {
			if tc.backend == "redis" {
				useRateLimitMiniRedis(t)
			}
			setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = tc.counts[0], tc.counts[1]
			setting.ModelRequestRateLimitGroup = map[string][2]int{"a": tc.counts, "b": tc.counts}
			userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
			router := gin.New()
			router.GET("/:group", func(c *gin.Context) {
				c.Set("id", userID)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, c.Param("group"))
			}, ModelRequestRateLimit(), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			for _, step := range steps {
				assert.Equal(t, step.want, performRateLimitRequest(router, "/"+step.group, "127.0.0.1:1000").Code, "group %s", step.group)
			}
		})
	}
}

// useModelRateLimitSettings enables the model request limiter on the memory
// backend with a one-minute global window and no group or model rules, and
// restores the previous settings afterwards.
func useModelRateLimitSettings(t *testing.T) {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousEnabled, previousMinutes := setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes
	previousTotal, previousSuccess := setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount
	previousGroups, previousRedisEnabled := setting.ModelRequestRateLimitGroup, common.RedisEnabled
	previousModels := setting.ModelRequestRateLimitModel2JSONString()
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = previousEnabled, previousMinutes
		setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = previousTotal, previousSuccess
		setting.ModelRequestRateLimitGroup, common.RedisEnabled = previousGroups, previousRedisEnabled
		require.NoError(t, setting.UpdateModelRequestRateLimitModelByJSONString(previousModels))
	})
	setting.ModelRequestRateLimitEnabled, setting.ModelRequestRateLimitDurationMinutes = true, 1
	setting.ModelRequestRateLimitCount, setting.ModelRequestRateLimitSuccessCount = 0, 1000
	setting.ModelRequestRateLimitGroup, common.RedisEnabled = map[string][2]int{}, false
	require.NoError(t, setting.UpdateModelRequestRateLimitModelByJSONString(`{}`))
}

func setModelRateLimitRules(t *testing.T, rules map[string]setting.ModelRequestRateLimitRule) {
	t.Helper()
	data, err := common.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateModelRequestRateLimitModelByJSONString(string(data)))
}

// newModelRuleTestRouter serves GET /?user=&model=[&outcome=failed] behind the
// global limiter, Distribute's record of the requested model, and the
// per-model limiter, like the relay routes.
func newModelRuleTestRouter() *gin.Engine {
	router := gin.New()
	router.GET("/", func(c *gin.Context) {
		userID, _ := strconv.Atoi(c.Query("user"))
		c.Set("id", userID)
		c.Set(common.RequestIdKey, "req-model-rule")
		common.SetContextKey(c, constant.ContextKeyTokenGroup, "vip")
	}, ModelRequestRateLimit(), func(c *gin.Context) {
		c.Set(string(constant.ContextKeyOriginalModel), c.Query("model"))
	}, PerModelRequestRateLimit(), func(c *gin.Context) {
		if c.Query("outcome") == "failed" {
			c.Status(http.StatusBadGateway)
			return
		}
		c.Status(http.StatusOK)
	})
	return router
}

func modelRuleRequest(router http.Handler, userID int, model string, outcome string) *httptest.ResponseRecorder {
	query := url.Values{"user": {strconv.Itoa(userID)}, "model": {model}}
	if outcome != "" {
		query.Set("outcome", outcome)
	}
	return performRateLimitRequest(router, "/?"+query.Encode(), "127.0.0.1:1000")
}

func requireRateLimitMessage(t *testing.T, response *httptest.ResponseRecorder, key string, params map[string]any) {
	t.Helper()
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, common.MessageWithRequestId(i18n.Translate("", key, params), "req-model-rule"), body.Error.Message)
}

// A model rule limits each user's requests to that model on their own: the
// user who used it up is rejected with a message naming the model and the
// rule's own window, while their other models and other users go on.
func TestPerModelRateLimitCountsEachUserAndModelSeparately(t *testing.T) {
	const model = "[按次]claude-opus-4-6"
	for _, tc := range []struct {
		backend    string
		rule       setting.ModelRequestRateLimitRule
		messageKey string
	}{
		{"memory", setting.ModelRequestRateLimitRule{DurationMinutes: 2, TotalCount: 1}, i18n.MsgModelRateLimitTotalReached},
		{"memory", setting.ModelRequestRateLimitRule{DurationMinutes: 2, SuccessCount: 1}, i18n.MsgModelRateLimitReached},
		// The Redis token bucket binds to the first Redis client it sees, so
		// total counts run on the memory backend only (see above).
		{"redis", setting.ModelRequestRateLimitRule{DurationMinutes: 2, SuccessCount: 1}, i18n.MsgModelRateLimitReached},
	} {
		t.Run(fmt.Sprintf("%s/%s", tc.backend, tc.messageKey), func(t *testing.T) {
			useModelRateLimitSettings(t)
			if tc.backend == "redis" {
				useRateLimitMiniRedis(t)
			}
			setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: tc.rule})
			limitedUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
			otherUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
			router := newModelRuleTestRouter()

			require.Equal(t, http.StatusOK, modelRuleRequest(router, limitedUser, model, "").Code)
			requireRateLimitMessage(t, modelRuleRequest(router, limitedUser, model, ""), tc.messageKey, map[string]any{"Model": model, "Minutes": 2, "Max": 1})
			assert.Equal(t, http.StatusOK, modelRuleRequest(router, limitedUser, "claude-opus-4-6", "").Code)
			assert.Equal(t, http.StatusOK, modelRuleRequest(router, otherUser, model, "").Code)
		})
	}
}

// The global or group limit and the model rule both count every request, so
// whichever allows fewer requests rejects first, with its own message.
func TestModelRateLimitStricterOfGroupAndModelRuleRejectsFirst(t *testing.T) {
	const model = "gpt-4o"
	for _, tc := range []struct {
		name       string
		backend    string
		group      [2]int // [total, success]
		rule       setting.ModelRequestRateLimitRule
		messageKey string
		params     map[string]any
	}{
		{"group total", "memory", [2]int{1, 1000}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 3}, i18n.MsgRateLimitTotalReached, map[string]any{"Minutes": 1, "Max": 1}},
		{"model total", "memory", [2]int{3, 1000}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 1}, i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": model, "Minutes": 1, "Max": 1}},
		{"group success", "memory", [2]int{0, 1}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 3}, i18n.MsgRateLimitReached, map[string]any{"Minutes": 1, "Max": 1}},
		{"model success", "memory", [2]int{0, 3}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 1}, i18n.MsgModelRateLimitReached, map[string]any{"Model": model, "Minutes": 1, "Max": 1}},
		{"group success", "redis", [2]int{0, 1}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 3}, i18n.MsgRateLimitReached, map[string]any{"Minutes": 1, "Max": 1}},
		{"model success", "redis", [2]int{0, 3}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 1}, i18n.MsgModelRateLimitReached, map[string]any{"Model": model, "Minutes": 1, "Max": 1}},
	} {
		t.Run(tc.backend+"/"+tc.name, func(t *testing.T) {
			useModelRateLimitSettings(t)
			if tc.backend == "redis" {
				useRateLimitMiniRedis(t)
			}
			setting.ModelRequestRateLimitGroup = map[string][2]int{"vip": tc.group}
			setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: tc.rule})
			userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
			router := newModelRuleTestRouter()

			require.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
			requireRateLimitMessage(t, modelRuleRequest(router, userID, model, ""), tc.messageKey, tc.params)
		})
	}
}

func TestPerModelRateLimitSuccessCountIgnoresFailedRequests(t *testing.T) {
	const model = "gpt-4o"
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			useModelRateLimitSettings(t)
			if backend == "redis" {
				useRateLimitMiniRedis(t)
			}
			setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 1, SuccessCount: 1}})
			userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
			router := newModelRuleTestRouter()

			assert.Equal(t, http.StatusBadGateway, modelRuleRequest(router, userID, model, "failed").Code)
			assert.Equal(t, http.StatusBadGateway, modelRuleRequest(router, userID, model, "failed").Code)
			assert.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
			assert.Equal(t, http.StatusTooManyRequests, modelRuleRequest(router, userID, model, "").Code)
		})
	}
}

// A model rule counts within its own window, not the global one: a success
// two minutes ago still counts for a three-minute rule although the global
// window is one minute, and the Redis key lives as long as the rule's window.
func TestPerModelRateLimitUsesTheRuleWindowInRedis(t *testing.T) {
	const model = "gpt-4o"
	useModelRateLimitSettings(t)
	redisServer, _ := useRateLimitMiniRedis(t)
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 3, SuccessCount: 1}})
	router := newModelRuleTestRouter()

	limitedUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
	limitedKey := fmt.Sprintf("rateLimit:%s:%d:model:%s", ModelRequestRateLimitSuccessCountMark, limitedUser, model)
	_, err := redisServer.Push(limitedKey, time.Now().UTC().Add(-2*time.Minute).Format(modelRateLimitTimeFormat))
	require.NoError(t, err)
	requireRateLimitMessage(t, modelRuleRequest(router, limitedUser, model, ""), i18n.MsgModelRateLimitReached, map[string]any{"Model": model, "Minutes": 3, "Max": 1})

	otherUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
	require.Equal(t, http.StatusOK, modelRuleRequest(router, otherUser, model, "").Code)
	assert.Equal(t, 3*time.Minute, redisServer.TTL(fmt.Sprintf("rateLimit:%s:%d:model:%s", ModelRequestRateLimitSuccessCountMark, otherUser, model)))
}
