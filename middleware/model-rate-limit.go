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

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	ModelRequestRateLimitCountMark        = "MRRL"
	ModelRequestRateLimitSuccessCountMark = "MRRLS"
	modelRateLimitTimeFormat              = "2006-01-02T15:04:05.000Z"
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
	// model is set for a per-model rule, whose rejections name it.
	model           string
	durationMinutes int
	totalMaxCount   int
	successMaxCount int
}

func (l modelRateLimit) totalReachedMessage(c *gin.Context) string {
	params := map[string]any{"Minutes": l.durationMinutes, "Max": l.totalMaxCount}
	if l.model == "" {
		return i18n.T(c, i18n.MsgRateLimitTotalReached, params)
	}
	params["Model"] = l.model
	return i18n.T(c, i18n.MsgModelRateLimitTotalReached, params)
}

func (l modelRateLimit) successReachedMessage(c *gin.Context) string {
	params := map[string]any{"Minutes": l.durationMinutes, "Max": l.successMaxCount}
	if l.model == "" {
		return i18n.T(c, i18n.MsgRateLimitReached, params)
	}
	params["Model"] = l.model
	return i18n.T(c, i18n.MsgModelRateLimitReached, params)
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
	duration := rateLimitDurationSeconds(limit.durationMinutes)
	return func(c *gin.Context) {
		ctx := context.Background()
		rdb := common.RDB

		// 1. 检查成功请求数限制
		successKey := fmt.Sprintf("rateLimit:%s:%s", ModelRequestRateLimitSuccessCountMark, limit.subject)
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
			totalKey := fmt.Sprintf("rateLimit:%s", limit.subject)
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

			if !allowed {
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, limit.totalReachedMessage(c))
				return
			}
		}

		// 4. 处理请求
		c.Next()

		// 5. 如果请求成功，记录成功请求
		if modelRequestSucceeded(c) {
			recordRedisRequest(ctx, rdb, successKey, limit.successMaxCount, duration)
		}
	}
}

// 内存限流处理器。Only the first Init of rateLimiter sets how long it keeps an
// idle key, so per-model rules pass the limiter of their window.
func memoryRateLimitHandler(rateLimiter *common.InMemoryRateLimiter, limit modelRateLimit) gin.HandlerFunc {
	rateLimiter.Init(time.Duration(limit.durationMinutes) * time.Minute)
	duration := rateLimitDurationSeconds(limit.durationMinutes)

	return func(c *gin.Context) {
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
		c.Next()

		// 4. 如果请求成功，记录到实际的成功请求计数中
		reservation.Complete(modelRequestSucceeded(c))
	}
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

// PerModelRequestRateLimit enforces the ModelRequestRateLimitModel rule of the
// model the client asked for. It runs after Distribute, which records that
// name before any model mapping, and inside ModelRequestRateLimit: a request
// must pass both, so the stricter limit rejects first. Each user's requests to
// a ruled model are counted on their own, within the rule's own window.
func PerModelRequestRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !setting.ModelRequestRateLimitEnabled {
			c.Next()
			return
		}
		modelName := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
		rule, found := setting.GetModelRequestRateLimitRule(modelName)
		if !found {
			c.Next()
			return
		}
		limit := modelRateLimit{
			subject:         strconv.Itoa(c.GetInt("id")) + ":model:" + modelName,
			model:           modelName,
			durationMinutes: rule.DurationMinutes,
			totalMaxCount:   rule.TotalCount,
			successMaxCount: rule.SuccessCount,
		}
		if common.RedisEnabled {
			redisRateLimitHandler(limit)(c)
			return
		}
		rateLimiter, _ := modelRuleMemoryLimiters.LoadOrStore(rule.DurationMinutes, new(common.InMemoryRateLimiter))
		memoryRateLimitHandler(rateLimiter.(*common.InMemoryRateLimiter), limit)(c)
	}
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
