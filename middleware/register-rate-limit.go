package middleware

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// Per-network limits of the registration pages. Each mark is a separate
// counter: registration submissions (including sign-ups through third-party
// login), registration email code requests and captcha images.
const (
	RegisterAttemptRateLimitMark = "RG"
	RegisterCodeRateLimitMark    = "RV"
	CaptchaRateLimitMark         = "CP"

	// While the registration limit is on, a network may load
	// max(CaptchaRateLimitMinNum, CaptchaRateLimitPerRegistration ×
	// RegisterIPRateLimitNum) captcha images per registration window.
	CaptchaRateLimitMinNum          = 30
	CaptchaRateLimitPerRegistration = 6

	// registerRateLimitKeyExpiration keeps an idle key in the in-memory
	// fallback for the longest registration window an administrator can set.
	registerRateLimitKeyExpiration = operation_setting.RegisterIPRateLimitMaxMinutes * time.Minute
)

// registerRateLimiter is the in-memory store of these limits. It is separate
// from inMemoryRateLimiter, which forgets a key after 20 idle minutes, because
// a registration window can last a whole day.
var registerRateLimiter = new(common.InMemoryRateLimiter)

// registerLimit is one of the administrator-configured registration limits.
type registerLimit struct {
	mark       string
	code       string
	messageKey string
}

var (
	registerAttemptLimit = registerLimit{
		mark:       RegisterAttemptRateLimitMark,
		code:       "REGISTER_RATE_LIMITED",
		messageKey: "Too many registration attempts from your network. Please try again in {{minutes}} minutes.",
	}
	registerCodeLimit = registerLimit{
		mark:       RegisterCodeRateLimitMark,
		code:       "REGISTER_CODE_RATE_LIMITED",
		messageKey: "Too many verification code requests from your network. Please try again in {{minutes}} minutes.",
	}
)

// registerLimitWindow returns the administrator's per-network registration
// limit: the requests a network may make per window and the window in
// seconds. The settings are read on every request, so a change applies
// without a restart. Saved values are validated; a hand-edited row is clamped
// into range.
func registerLimitWindow() (int, int64) {
	maxRequests := min(max(common.RegisterIPRateLimitNum, 1), operation_setting.RegisterIPRateLimitMaxNum)
	minutes := min(max(common.RegisterIPRateLimitMinutes, 1), operation_setting.RegisterIPRateLimitMaxMinutes)
	return maxRequests, int64(minutes) * 60
}

// take counts one request of the client network while the registration limit
// is on. It returns 0 when the request is allowed, otherwise the seconds until
// the network may try again.
func (limit registerLimit) take(c *gin.Context) int64 {
	if !common.RegisterIPRateLimitEnabled {
		return 0
	}
	maxRequests, window := registerLimitWindow()
	allowed, retryAfter := takeClientNetworkLimit(c, limit.mark, maxRequests, window)
	if allowed {
		return 0
	}
	return retryAfter
}

// reject answers a refused request with 429, the limit's code and the whole
// minutes left to wait.
func (limit registerLimit) reject(c *gin.Context, retryAfter int64) {
	minutes := max((retryAfter+59)/60, 1)
	message := common.NewMessage(limit.messageKey, map[string]any{"minutes": minutes})
	writeClientNetworkRateLimited(c, retryAfter, message, gin.H{"code": limit.code})
}

func (limit registerLimit) handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if retryAfter := limit.take(c); retryAfter > 0 {
			limit.reject(c, retryAfter)
			return
		}
		c.Next()
	}
}

// RegisterAttemptRateLimit counts every registration submission of the client
// network (RG). It runs before any other check of the request, so wrong
// captchas, wrong codes and taken usernames all count.
func RegisterAttemptRateLimit() gin.HandlerFunc {
	return registerAttemptLimit.handler()
}

// RegisterCodeRateLimit counts every registration email code request of the
// client network (RV).
func RegisterCodeRateLimit() gin.HandlerFunc {
	return registerCodeLimit.handler()
}

// TakeRegisterAttempt counts a sign-up through a third-party login that is
// about to create an account, on the same counter as registration
// submissions. It returns 0 when the sign-up is allowed, otherwise the
// seconds to pass to RejectRegisterAttempt.
func TakeRegisterAttempt(c *gin.Context) int64 {
	return registerAttemptLimit.take(c)
}

// RejectRegisterAttempt answers a sign-up that TakeRegisterAttempt refused.
func RejectRegisterAttempt(c *gin.Context, retryAfter int64) {
	registerAttemptLimit.reject(c, retryAfter)
}

// CaptchaImagesPerCriticalRequest is the room for one request the critical
// rate limit lets an IP make on the sign-in and sign-up pages: the image it
// answers, the one shown after a refusal, and one loaded but never submitted
// (an expired image, a page left early).
const CaptchaImagesPerCriticalRequest = 3

// captchaImageLimit returns the captcha images a client network may load per
// window (in seconds); ok is false when images are not limited.
//
// While the per-network registration limit is on: at least
// CaptchaRateLimitMinNum images and CaptchaRateLimitPerRegistration for each
// registration the network may submit in the registration window, plus
// CaptchaImagesPerCriticalRequest for each request the critical rate limit
// allows in that window while the login or password reset captcha is on.
//
// While it is off: CaptchaImagesPerCriticalRequest per request the critical
// rate limit allows (at least CaptchaRateLimitMinNum), per critical window,
// while any captcha is on, so a flood of images cannot evict the challenges
// users are answering. With the critical rate limit off as well, images are
// not limited: the server may then sit behind a proxy it does not trust,
// where every visitor has the proxy's address and any per-IP image limit
// would close sign-in for the whole site.
func captchaImageLimit() (maxImages int, window int64, ok bool) {
	signIn := common.LoginCaptchaEnabled || common.PasswordResetCaptchaEnabled
	criticalNum, criticalWindow := common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	if criticalNum <= 0 {
		criticalNum = 20
	}
	if criticalWindow <= 0 {
		criticalWindow = 20 * 60
	}
	if common.RegisterIPRateLimitEnabled {
		maxRequests, registerWindow := registerLimitWindow()
		maxImages = max(CaptchaRateLimitMinNum, CaptchaRateLimitPerRegistration*maxRequests)
		if signIn {
			maxImages += CaptchaImagesPerCriticalRequest * criticalNum * int((registerWindow+criticalWindow-1)/criticalWindow)
		}
		return maxImages, registerWindow, true
	}
	if common.CriticalRateLimitEnable && (common.RegisterCaptchaEnabled || signIn) {
		return max(CaptchaRateLimitMinNum, CaptchaImagesPerCriticalRequest*criticalNum), criticalWindow, true
	}
	return 0, 0, false
}

// CaptchaRateLimit limits captcha images per client network (CP) as
// captchaImageLimit says. The settings are read on every request.
func CaptchaRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		maxImages, window, ok := captchaImageLimit()
		if !ok {
			c.Next()
			return
		}
		allowed, retryAfter := takeClientNetworkLimit(c, CaptchaRateLimitMark, maxImages, window)
		if !allowed {
			writeClientNetworkRateLimited(c, retryAfter, common.NewMessage("Too many captcha requests. Please try again later."))
			return
		}
		c.Next()
	}
}

// clientNetworkKey identifies the client network of an address: an IPv4
// address as is, an IPv6 address by its /64 prefix, which one subscriber
// usually controls as a whole.
func clientNetworkKey(clientIP string) string {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return clientIP
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// takeClientNetworkLimit counts one request of mark for the client network in
// a Redis fixed window, or in registerRateLimiter when Redis is off or
// failing. It reports whether the request is allowed and, when it is not,
// the seconds until the window frees a slot.
func takeClientNetworkLimit(c *gin.Context, mark string, maxRequests int, duration int64) (bool, int64) {
	key := clientNetworkKey(c.ClientIP())
	if common.RedisEnabled {
		allowed, _, ttlSeconds, err := redisFixedWindowTake(c.Request.Context(), redisIPRateLimitKey(mark, key), maxRequests, duration)
		if err == nil {
			if allowed {
				return true, 0
			}
			if ttlSeconds <= 0 {
				ttlSeconds = duration
			}
			return false, ttlSeconds
		}
		logger.LogWarn(c.Request.Context(), common.LogText("rate limit check failed, using the in-memory limiter (mark=%s): %v", mark, err))
	}
	registerRateLimiter.Init(registerRateLimitKeyExpiration)
	return registerRateLimiter.RequestWithRetryAfter(mark+":"+key, maxRequests, duration)
}

// writeClientNetworkRateLimited rejects a request with 429, Retry-After and a
// web console message.
func writeClientNetworkRateLimited(c *gin.Context, retryAfter int64, message *common.Message, fields ...gin.H) {
	c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
	c.Header("Cache-Control", "no-store")
	common.ApiErrorStatus(c, http.StatusTooManyRequests, message, fields...)
	c.Abort()
}
