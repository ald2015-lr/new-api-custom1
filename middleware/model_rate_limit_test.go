package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
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

type modelRateLimitRedisFixture struct {
	server *miniredis.Miniredis
	client *redis.Client
}

// The token bucket of common/limiter binds to the first Redis client it is
// given for the rest of the process, so every model limiter test runs on this
// one server and client, which stay open.
var startModelRateLimitRedis = sync.OnceValues(func() (modelRateLimitRedisFixture, error) {
	server, err := miniredis.Run()
	if err != nil {
		return modelRateLimitRedisFixture{}, err
	}
	return modelRateLimitRedisFixture{server: server, client: redis.NewClient(&redis.Options{Addr: server.Addr()})}, nil
})

// useModelRateLimitRedis switches the model limiter to the shared Redis
// server, emptied, until the test ends.
func useModelRateLimitRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	fixture, err := startModelRateLimitRedis()
	require.NoError(t, err)
	fixture.server.FlushAll()
	previousRedisEnabled, previousRedisClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, fixture.client
	t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedisEnabled, previousRedisClient })
	return fixture.server
}

func TestModelRateLimitStreamFailuresDoNotConsumeSuccessLimit(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, backend := range []string{"memory", "redis"} {
		for _, totalLimit := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/total=%d", backend, totalLimit), func(t *testing.T) {
				subject := strconv.Itoa(7200000 + int(modelRateLimitTestUsers.Add(1)))
				limit := modelRateLimit{subject: subject, durationMinutes: 1, totalMaxCount: totalLimit, successMaxCount: 1}
				handler := memoryRateLimitHandler(&inMemoryRateLimiter, limit)
				if backend == "redis" {
					useModelRateLimitRedis(t)
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
	for _, tc := range []struct {
		backend, limit string
		counts         [2]int // [total, success] for groups a and b and for the global limit
	}{
		{"memory", "success", [2]int{0, 1}},
		{"redis", "success", [2]int{0, 1}},
		{"memory", "total", [2]int{1, 1000}},
		{"redis", "total", [2]int{1, 1000}},
	} {
		t.Run(tc.backend+"/"+tc.limit, func(t *testing.T) {
			if tc.backend == "redis" {
				useModelRateLimitRedis(t)
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

// newModelRuleTestRouter serves GET /?user=&model=[&outcome=failed][&group=]
// behind the global limiter, Distribute's record of the requested model, and
// the per-model limiter, like the relay routes. The token group is vip unless
// the request names another.
func newModelRuleTestRouter() *gin.Engine {
	router := gin.New()
	router.GET("/", func(c *gin.Context) {
		userID, _ := strconv.Atoi(c.Query("user"))
		c.Set("id", userID)
		c.Set(common.RequestIdKey, "req-model-rule")
		common.SetContextKey(c, constant.ContextKeyTokenGroup, c.DefaultQuery("group", "vip"))
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
		{"redis", setting.ModelRequestRateLimitRule{DurationMinutes: 2, TotalCount: 1}, i18n.MsgModelRateLimitTotalReached},
		{"redis", setting.ModelRequestRateLimitRule{DurationMinutes: 2, SuccessCount: 1}, i18n.MsgModelRateLimitReached},
	} {
		t.Run(fmt.Sprintf("%s/%s", tc.backend, tc.messageKey), func(t *testing.T) {
			useModelRateLimitSettings(t)
			if tc.backend == "redis" {
				useModelRateLimitRedis(t)
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
		{"group total", "redis", [2]int{1, 1000}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 3}, i18n.MsgRateLimitTotalReached, map[string]any{"Minutes": 1, "Max": 1}},
		{"model total", "redis", [2]int{3, 1000}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 1}, i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": model, "Minutes": 1, "Max": 1}},
		{"group success", "redis", [2]int{0, 1}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 3}, i18n.MsgRateLimitReached, map[string]any{"Minutes": 1, "Max": 1}},
		{"model success", "redis", [2]int{0, 3}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 1}, i18n.MsgModelRateLimitReached, map[string]any{"Model": model, "Minutes": 1, "Max": 1}},
	} {
		t.Run(tc.backend+"/"+tc.name, func(t *testing.T) {
			useModelRateLimitSettings(t)
			if tc.backend == "redis" {
				useModelRateLimitRedis(t)
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
				useModelRateLimitRedis(t)
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
// window is one minute, and its Redis keys live as long as the rule's window,
// also when the rule rejects.
func TestPerModelRateLimitUsesTheRuleWindowInRedis(t *testing.T) {
	const model = "gpt-4o"
	useModelRateLimitSettings(t)
	redisServer := useModelRateLimitRedis(t)
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 3, SuccessCount: 1}})
	router := newModelRuleTestRouter()

	limitedUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
	limitedKey := fmt.Sprintf("rateLimit:MRRLS:MRRLM:%d:model:%s", limitedUser, model)
	_, err := redisServer.Push(limitedKey, time.Now().UTC().Add(-2*time.Minute).Format(modelRateLimitTimeFormat))
	require.NoError(t, err)
	requireRateLimitMessage(t, modelRuleRequest(router, limitedUser, model, ""), i18n.MsgModelRateLimitReached, map[string]any{"Model": model, "Minutes": 3, "Max": 1})
	assert.Equal(t, 3*time.Minute, redisServer.TTL(limitedKey))

	otherUser := 7200000 + int(modelRateLimitTestUsers.Add(1))
	require.Equal(t, http.StatusOK, modelRuleRequest(router, otherUser, model, "").Code)
	assert.Equal(t, 3*time.Minute, redisServer.TTL(fmt.Sprintf("rateLimit:MRRLS:MRRLM:%d:model:%s", otherUser, model)))
}

// The token bucket of a total count keeps no expiry of its own. A bucket idle
// for its whole window is full again, so its key expires then and a removed
// rule leaves nothing behind; the global bucket expires with the global window.
func TestModelRateLimitTotalCountKeysExpireWithTheirWindow(t *testing.T) {
	const model = "gpt-4o"
	useModelRateLimitSettings(t)
	redisServer := useModelRateLimitRedis(t)
	setting.ModelRequestRateLimitCount = 5
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 2, TotalCount: 5}})
	userID := 7200000 + int(modelRateLimitTestUsers.Add(1))

	require.Equal(t, http.StatusOK, modelRuleRequest(newModelRuleTestRouter(), userID, model, "").Code)
	assert.Equal(t, 2*time.Minute, redisServer.TTL(fmt.Sprintf("rateLimit:MRRLM:%d:model:%s", userID, model)))
	assert.Equal(t, time.Minute, redisServer.TTL(fmt.Sprintf("rateLimit:%d", userID)))
}

// A model rule's subject "<user>:model:<name>" is also the subject of a group
// named "model:<name>". Their counts stay apart: requests of that group to
// other models do not use up the user's rule for the model.
func TestPerModelRateLimitDoesNotShareCountsWithAGroupOfTheSameSubject(t *testing.T) {
	const model = "gpt-4o"
	for _, backend := range []string{"memory", "redis"} {
		for _, tc := range []struct {
			name  string
			group [2]int // [total, success]
			rule  setting.ModelRequestRateLimitRule
		}{
			{"success", [2]int{0, 100}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, SuccessCount: 1}},
			{"total", [2]int{2, 1000}, setting.ModelRequestRateLimitRule{DurationMinutes: 1, TotalCount: 1}},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				useModelRateLimitSettings(t)
				if backend == "redis" {
					useModelRateLimitRedis(t)
				}
				setting.ModelRequestRateLimitGroup = map[string][2]int{"model:" + model: tc.group}
				setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: tc.rule})
				userID := strconv.Itoa(7200000 + int(modelRateLimitTestUsers.Add(1)))
				router := newModelRuleTestRouter()

				for _, requested := range []string{"other-model", model} {
					query := url.Values{"user": {userID}, "model": {requested}, "group": {"model:" + model}}
					assert.Equal(t, http.StatusOK, performRateLimitRequest(router, "/?"+query.Encode(), "127.0.0.1:1000").Code, requested)
				}
			})
		}
	}
}

// A variant of a model, named with @ modifiers or a legacy suffix, reaches the
// same channels as the model, so it counts under the model's rule and the
// rejection names that rule. A [...] line tag stays part of the name.
func TestPerModelRateLimitCountsModelVariantsUnderTheModelsRule(t *testing.T) {
	for _, tc := range []struct{ rule, variant string }{
		{"gpt-4o", "gpt-4o@temperature:1"},
		{"gpt-4o", "gpt-4o@effort:high@temperature:0.7"},
		{"claude-opus-4-6", "claude-opus-4-6-thinking"},
		{"[按次]claude-opus-4-6", "[按次]claude-opus-4-6@effort:high"},
	} {
		t.Run(tc.variant, func(t *testing.T) {
			useModelRateLimitSettings(t)
			setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{tc.rule: {DurationMinutes: 1, TotalCount: 1}})
			userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
			router := newModelRuleTestRouter()

			require.Equal(t, http.StatusOK, modelRuleRequest(router, userID, tc.variant, "").Code)
			requireRateLimitMessage(t, modelRuleRequest(router, userID, tc.rule, ""), i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": tc.rule, "Minutes": 1, "Max": 1})
		})
	}
}

// A variant with a rule of its own counts under that rule only.
func TestPerModelRateLimitPrefersTheExactRuleOfAVariant(t *testing.T) {
	const model, variant = "gpt-4o", "gpt-4o@temperature:1"
	useModelRateLimitSettings(t)
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{
		model:   {DurationMinutes: 1, TotalCount: 1},
		variant: {DurationMinutes: 1, TotalCount: 1},
	})
	userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
	router := newModelRuleTestRouter()

	require.Equal(t, http.StatusOK, modelRuleRequest(router, userID, variant, "").Code)
	require.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
	requireRateLimitMessage(t, modelRuleRequest(router, userID, variant, ""), i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": variant, "Minutes": 1, "Max": 1})
}

func TestPerModelRateLimitDoesNotApplyWhileRateLimitingIsOff(t *testing.T) {
	const model = "gpt-4o"
	useModelRateLimitSettings(t)
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 1, TotalCount: 1}})
	setting.ModelRequestRateLimitEnabled = false
	userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
	router := newModelRuleTestRouter()

	assert.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
	assert.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
}

// On the memory backend a rule's counts live in a limiter first initialized
// with the rule's window. The shared limiter drops a key idle for the global
// window (one minute here), which would forget requests still inside the
// rule's seven minutes.
func TestPerModelRateLimitKeepsMemoryCountsInALimiterOfTheRuleWindow(t *testing.T) {
	const model = "gpt-4o"
	useModelRateLimitSettings(t)
	setModelRateLimitRules(t, map[string]setting.ModelRequestRateLimitRule{model: {DurationMinutes: 7, TotalCount: 1}})
	userID := 7200000 + int(modelRateLimitTestUsers.Add(1))
	router := newModelRuleTestRouter()

	require.Equal(t, http.StatusOK, modelRuleRequest(router, userID, model, "").Code)
	requireRateLimitMessage(t, modelRuleRequest(router, userID, model, ""), i18n.MsgModelRateLimitTotalReached, map[string]any{"Model": model, "Minutes": 7, "Max": 1})
	ruleLimiter, found := modelRuleMemoryLimiters.Load(7)
	require.True(t, found)
	totalKey := fmt.Sprintf("%s%d:model:%s", ModelRequestRateLimitCountMark, userID, model)
	assert.False(t, ruleLimiter.(*common.InMemoryRateLimiter).Request(totalKey, 1, 7*60), "the rule window limiter holds the request")
	assert.True(t, inMemoryRateLimiter.Request(totalKey, 1, 7*60), "the shared limiter holds no request of the rule")
}
