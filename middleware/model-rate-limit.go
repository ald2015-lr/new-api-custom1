package middleware

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/common/limiter"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	ModelRequestRateLimitCountMark        = "MRRL"
	ModelRequestRateLimitSuccessCountMark = "MRRLS"
	// ModelRequestRateLimitModelMark namespaces the Redis keys of per-model
	// rules. Their subject "<uid>:model:<name>" is also the subject of a group
	// named "model:<name>", whose keys must stay apart.
	ModelRequestRateLimitModelMark = "MRRLM"
	modelRateLimitTimeFormat       = "2006-01-02T15:04:05.000Z"
)

// modelRuleMemoryLimiters holds one in-memory limiter per per-model rule
// window, keyed by the window in minutes. A limiter drops a key once it has
// been idle for the expiration of its first Init. For the shared limiter that
// is whichever caller ran first, so a rule with a longer window would forget
// requests still inside it; each of these is first initialized with its window.
var modelRuleMemoryLimiters sync.Map

// modelRateLimit is one limit the model request limiter enforces for a
// subject: the global or group limit, or a per-model rule.
type modelRateLimit struct {
	subject string
	// model is set for a per-model rule, whose rejections name it: the key of
	// an exact rule, or the requested model for a prefix or suffix rule.
	model string
	// rule is the pattern of a prefix or suffix rule, such as "[按次]*" or
	// "*-thinking", which its rejections name, and shared whether it counts
	// all the models it matches together.
	rule            string
	shared          bool
	durationMinutes int
	totalMaxCount   int
	successMaxCount int
}

func (l modelRateLimit) totalReachedMessage(c *gin.Context) string {
	key := i18n.MsgRateLimitTotalReached
	switch {
	case l.rule != "" && l.shared:
		key = i18n.MsgModelRuleSharedTotalReached
	case l.rule != "":
		key = i18n.MsgModelRuleTotalReached
	case l.model != "":
		key = i18n.MsgModelRateLimitTotalReached
	}
	return i18n.T(c, key, map[string]any{"Minutes": l.durationMinutes, "Max": l.totalMaxCount, "Model": l.model, "Rule": l.rule})
}

func (l modelRateLimit) successReachedMessage(c *gin.Context) string {
	key := i18n.MsgRateLimitReached
	switch {
	case l.rule != "" && l.shared:
		key = i18n.MsgModelRuleSharedReached
	case l.rule != "":
		key = i18n.MsgModelRuleReached
	case l.model != "":
		key = i18n.MsgModelRateLimitReached
	}
	return i18n.T(c, key, map[string]any{"Minutes": l.durationMinutes, "Max": l.successMaxCount, "Model": l.model, "Rule": l.rule})
}

// 检查Redis中的请求限制
func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) (bool, error) {
	// 如果maxCount为0，表示不限制
	if maxCount == 0 {
		return true, nil
	}

	// 获取当前计数
	length, err := rdb.LLen(ctx, key).Result()
	if err != nil {
		return false, err
	}

	// 如果未达到限制，允许请求
	if length < int64(maxCount) {
		return true, nil
	}

	// 检查时间窗口
	oldTimeStr, _ := rdb.LIndex(ctx, key, -1).Result()
	oldTime, err := time.Parse(modelRateLimitTimeFormat, oldTimeStr)
	if err != nil {
		return false, err
	}

	nowTimeStr := time.Now().UTC().Format(modelRateLimitTimeFormat)
	nowTime, err := time.Parse(modelRateLimitTimeFormat, nowTimeStr)
	if err != nil {
		return false, err
	}
	// 如果在时间窗口内已达到限制，拒绝请求
	subTime := nowTime.Sub(oldTime).Seconds()
	if int64(subTime) < duration {
		rdb.Expire(ctx, key, time.Duration(duration)*time.Second)
		return false, nil
	}

	return true, nil
}

// 记录Redis请求
func recordRedisRequest(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) {
	// 如果maxCount为0，不记录请求
	if maxCount == 0 {
		return
	}

	now := time.Now().UTC().Format(modelRateLimitTimeFormat)
	rdb.LPush(ctx, key, now)
	rdb.LTrim(ctx, key, 0, int64(maxCount-1))
	rdb.Expire(ctx, key, time.Duration(duration)*time.Second)
}

// Redis限流处理器
func redisRateLimitHandler(limit modelRateLimit) gin.HandlerFunc {
	return func(c *gin.Context) {
		enforceRedisRateLimit(c, limit, c.Next)
	}
}

// enforceRedisRateLimit checks limit on Redis, runs next when it allows the
// request and then records a success.
func enforceRedisRateLimit(c *gin.Context, limit modelRateLimit, next func()) {
	duration := rateLimitDurationSeconds(limit.durationMinutes)
	keySubject := limit.subject
	if limit.model != "" || limit.rule != "" {
		keySubject = ModelRequestRateLimitModelMark + ":" + limit.subject
	}
	ctx := context.Background()
	rdb := common.RDB

	// 1. 检查成功请求数限制
	successKey := fmt.Sprintf("rateLimit:%s:%s", ModelRequestRateLimitSuccessCountMark, keySubject)
	allowed, err := checkRedisRateLimit(ctx, rdb, successKey, limit.successMaxCount, duration)
	if err != nil {
		fmt.Println(common.LogText("failed to check the successful request limit: %s", err.Error()))
		abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
		return
	}
	if !allowed {
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, limit.successReachedMessage(c))
		return
	}

	//2.检查总请求数限制并记录总请求（当totalMaxCount为0时会自动跳过，使用令牌桶限流器
	if limit.totalMaxCount > 0 {
		totalKey := fmt.Sprintf("rateLimit:%s", keySubject)
		// 初始化
		tb := limiter.New(ctx, rdb)
		allowed, err = tb.Allow(
			ctx,
			totalKey,
			limiter.WithCapacity(rateLimitCapacity(limit.totalMaxCount, duration)),
			limiter.WithRate(int64(limit.totalMaxCount)),
			limiter.WithRequested(duration),
		)

		if err != nil {
			fmt.Println(common.LogText("failed to check the total request limit: %s", err.Error()))
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}
		// The bucket script sets no expiry. A bucket idle for a whole window
		// has refilled to capacity, the same as a missing key, so the key
		// expires then and a removed rule leaves no key behind.
		rdb.Expire(ctx, totalKey, time.Duration(duration)*time.Second)

		if !allowed {
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, limit.totalReachedMessage(c))
			return
		}
	}

	// 4. 处理请求
	next()

	// 5. 如果请求成功，记录成功请求
	if modelRequestSucceeded(c) {
		recordRedisRequest(ctx, rdb, successKey, limit.successMaxCount, duration)
	}
}

// 内存限流处理器。Only the first Init of rateLimiter sets how long it keeps an
// idle key, so per-model rules pass the limiter of their window.
func memoryRateLimitHandler(rateLimiter *common.InMemoryRateLimiter, limit modelRateLimit) gin.HandlerFunc {
	return func(c *gin.Context) {
		enforceMemoryRateLimit(c, rateLimiter, limit, c.Next)
	}
}

// enforceMemoryRateLimit checks limit on rateLimiter, runs next when it
// allows the request and then records a success.
func enforceMemoryRateLimit(c *gin.Context, rateLimiter *common.InMemoryRateLimiter, limit modelRateLimit, next func()) {
	rateLimiter.Init(time.Duration(limit.durationMinutes) * time.Minute)
	duration := rateLimitDurationSeconds(limit.durationMinutes)
	totalKey := ModelRequestRateLimitCountMark + limit.subject
	successKey := ModelRequestRateLimitSuccessCountMark + limit.subject

	// 1. 检查总请求数限制（当totalMaxCount为0时跳过）
	if limit.totalMaxCount > 0 && !rateLimiter.Request(totalKey, limit.totalMaxCount, duration) {
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, limit.totalReachedMessage(c))
		return
	}

	var reservation *common.RateLimitReservation
	if limit.successMaxCount > 0 {
		reservation = rateLimiter.Reserve(successKey, limit.successMaxCount, duration)
		if reservation == nil {
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, limit.successReachedMessage(c))
			return
		}
		defer reservation.Complete(false)
	}

	// 3. 处理请求
	next()

	// 4. 如果请求成功，记录到实际的成功请求计数中
	reservation.Complete(modelRequestSucceeded(c))
}

func modelRequestSucceeded(c *gin.Context) bool {
	status, _ := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
	return c.Writer.Status() < 400 && !status.ResponseFailed()
}

// ModelRequestRateLimit 模型请求限流中间件
func ModelRequestRateLimit() func(c *gin.Context) {
	return func(c *gin.Context) {
		// 在每个请求时检查是否启用限流
		if !setting.ModelRequestRateLimitEnabled {
			c.Next()
			return
		}

		// 计算限流参数
		limit := modelRateLimit{
			subject:         strconv.Itoa(c.GetInt("id")),
			durationMinutes: setting.ModelRequestRateLimitDurationMinutes,
			totalMaxCount:   setting.ModelRequestRateLimitCount,
			successMaxCount: setting.ModelRequestRateLimitSuccessCount,
		}

		// 获取分组
		group := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		if group == "" {
			group = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		}

		// 获取分组的限流配置。配置了独立限流的分组按用户和分组单独计数，
		// 其余分组共用该用户的全局计数
		groupTotalCount, groupSuccessCount, found := setting.GetGroupRateLimit(group)
		if found {
			limit.totalMaxCount = groupTotalCount
			limit.successMaxCount = groupSuccessCount
			limit.subject += ":" + group
		}

		// 根据存储类型选择并执行限流处理器
		if common.RedisEnabled {
			redisRateLimitHandler(limit)(c)
		} else {
			memoryRateLimitHandler(&inMemoryRateLimiter, limit)(c)
		}
	}
}

// PerModelRequestRateLimit enforces the ModelRequestRateLimitModel rules of
// the model the client asked for. It runs after Distribute, which records that
// name before any model mapping, and inside ModelRequestRateLimit: a request
// must pass both, so the stricter limit rejects first. Each user's requests
// are counted on their own, within each rule's own window.
//
// An exact rule matches like the token model limit (TokenModelLimitAllows):
// the exact name, then the wildcard-normalized name, then the routing name, so
// a variant named with @ modifiers or a legacy suffix such as -thinking, which
// reaches the same channels as its model, counts under the model's rule
// unless it has a rule of its own. The routing name keeps a [...] line tag, so
// a tagged line keeps its own rule. A prefix or suffix rule matches when its
// key starts or ends the requested name, that name without its @ modifiers,
// or the routing name; "[按次]" matches every model of that line, and
// "-thinking" matches claude-opus-4-6-thinking@effort:high, whose routing
// name claude-opus-4-6 has dropped the suffix.
//
// Every matching rule applies: the exact rule first, then the prefix and
// suffix rules by key, each with its own window and counts. The first one
// that is used up rejects the request. Like a model rule after the global
// limit, the rules checked before it have then counted the request in their
// totals, and none records a success.
func PerModelRequestRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !setting.ModelRequestRateLimitEnabled {
			c.Next()
			return
		}
		requested := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
		routing := ratio_setting.RoutingMatchModelName(requested)
		matches := setting.FindModelRequestRateLimitRules(requested, ratio_setting.FormatMatchingModelName(requested), hostreasoning.ModelNameWithoutModifiers(requested), routing)
		if len(matches) == 0 {
			c.Next()
			return
		}
		userID := strconv.Itoa(c.GetInt("id"))
		limits := make([]modelRateLimit, len(matches))
		for i, match := range matches {
			limits[i] = modelRuleLimit(userID, requested, routing, match)
		}
		enforceModelRuleLimits(c, limits)
	}
}

// modelRuleLimit is the limit a per-model rule sets for a user's request for
// model requested, whose routing name is routing. Its subject, which follows
// the user id, is
//
//	<uid>:model:<key>                                  exact rule
//	<uid>:model-prefix:<key>                           prefix rule counting its models together
//	<uid>:model-suffix:<key>                           suffix rule counting its models together
//	<uid>:model-prefix-each:<len(key)>:<key>:<routing>  prefix rule counting each model on its own
//	<uid>:model-suffix-each:<len(key)>:<key>:<routing>  suffix rule counting each model on its own
//
// The word after the user id tells the kinds apart, and the byte length of the
// key keeps a key that contains ":" from running into the model name, so no
// two rules or models share a count. Counting each model by its routing name
// counts the @ modifier variants of a model together, as an exact rule does.
// The exact subject is the one rules had before match modes existed, so their
// counts carry over. Redis keys of all of them are namespaced with
// ModelRequestRateLimitModelMark, apart from group subjects "<uid>:<group>".
func modelRuleLimit(userID, requested, routing string, match setting.ModelRequestRateLimitMatch) modelRateLimit {
	limit := modelRateLimit{
		subject:         userID + ":model:" + match.Key,
		model:           match.Key,
		durationMinutes: match.Rule.DurationMinutes,
		totalMaxCount:   match.Rule.TotalCount,
		successMaxCount: match.Rule.SuccessCount,
	}
	if !match.Rule.IsPattern() {
		return limit
	}
	kind, pattern := "model-prefix", match.Key+"*"
	if match.Rule.Match == setting.ModelRateLimitMatchSuffix {
		kind, pattern = "model-suffix", "*"+match.Key
	}
	limit.model, limit.rule, limit.shared = requested, pattern, match.Rule.Shared
	if match.Rule.Shared {
		limit.subject = userID + ":" + kind + ":" + match.Key
	} else {
		limit.subject = userID + ":" + kind + "-each:" + strconv.Itoa(len(match.Key)) + ":" + match.Key + ":" + routing
	}
	return limit
}

// enforceModelRuleLimits enforces limits in order, each one around the next,
// and runs the rest of the handler chain inside the last.
func enforceModelRuleLimits(c *gin.Context, limits []modelRateLimit) {
	if len(limits) == 0 {
		c.Next()
		return
	}
	next := func() { enforceModelRuleLimits(c, limits[1:]) }
	if common.RedisEnabled {
		enforceRedisRateLimit(c, limits[0], next)
		return
	}
	rateLimiter, _ := modelRuleMemoryLimiters.LoadOrStore(limits[0].durationMinutes, new(common.InMemoryRateLimiter))
	enforceMemoryRateLimit(c, rateLimiter.(*common.InMemoryRateLimiter), limits[0], next)
}

func rateLimitDurationSeconds(durationMinutes int) int64 {
	if durationMinutes <= 0 {
		return 0
	}
	minutes := int64(durationMinutes)
	if minutes > math.MaxInt64/60 {
		return math.MaxInt64
	}
	return minutes * 60
}

func rateLimitCapacity(count int, durationSeconds int64) int64 {
	if count <= 0 || durationSeconds <= 0 {
		return 0
	}
	c := int64(count)
	if c > math.MaxInt64/durationSeconds {
		return math.MaxInt64
	}
	return c * durationSeconds
}
