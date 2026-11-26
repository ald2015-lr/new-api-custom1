package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func performWebLimitRequest(router http.Handler, method, accept, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/console/log", nil)
	request.RemoteAddr = remoteAddr
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

const browserPageAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// A rate-limited page load (GET or HEAD that accepts HTML) gets a short page
// in Chinese and English that reloads itself after Retry-After, at most 60
// seconds, instead of the empty body browsers show as an error page. Other
// requests keep the empty 429. Both backends answer the same way.
func TestGlobalWebRateLimitAnswersPageLoadsWithARetryPage(t *testing.T) {
	for _, backend := range []string{"redis", "memory"} {
		for _, window := range []int64{45, 180} {
			t.Run(fmt.Sprintf("%s/%ds", backend, window), func(t *testing.T) {
				if backend == "redis" {
					useRateLimitMiniRedis(t)
				} else {
					useMemoryRateLimitBackend(t)
				}
				useGlobalWebRateLimit(t, 1, window)
				gin.SetMode(gin.TestMode)
				router := gin.New()
				require.NoError(t, router.SetTrustedProxies(nil))
				router.Any("/console/log", GlobalWebRateLimit(emptyFrontendFS(t)), func(c *gin.Context) {
					c.Status(http.StatusNoContent)
				})
				remoteAddr := uniqueTestClientIP() + ":4321"
				require.Equal(t, http.StatusNoContent, performWebLimitRequest(router, http.MethodGet, browserPageAccept, remoteAddr).Code)

				page := performWebLimitRequest(router, http.MethodGet, browserPageAccept, remoteAddr)
				assert.Equal(t, http.StatusTooManyRequests, page.Code)
				assert.Equal(t, "text/html; charset=utf-8", page.Header().Get("Content-Type"))
				assert.Equal(t, "no-store", page.Header().Get("Cache-Control"))
				assert.Equal(t, fmt.Sprint(window), page.Header().Get("Retry-After"))
				refresh := min(window, 60)
				body := page.Body.String()
				assert.Contains(t, body, fmt.Sprintf(`<meta http-equiv="refresh" content="%d">`, refresh))
				assert.Contains(t, body, "访问过于频繁")
				assert.Contains(t, body, fmt.Sprintf("%d 秒后自动重试", refresh))
				assert.Contains(t, body, fmt.Sprintf("Too many requests. Retrying automatically in %d seconds.", refresh))
				assert.Less(t, len(body), 1024)

				head := performWebLimitRequest(router, http.MethodHead, browserPageAccept, remoteAddr)
				assert.Equal(t, http.StatusTooManyRequests, head.Code)
				assert.Equal(t, "text/html; charset=utf-8", head.Header().Get("Content-Type"))

				for _, other := range []struct{ method, accept string }{
					{http.MethodGet, "application/json, text/plain, */*"},
					{http.MethodGet, "*/*"},
					{http.MethodGet, ""},
					{http.MethodPost, browserPageAccept},
				} {
					response := performWebLimitRequest(router, other.method, other.accept, remoteAddr)
					assert.Equal(t, http.StatusTooManyRequests, response.Code, "%s %q", other.method, other.accept)
					assert.Empty(t, response.Body.String(), "%s %q", other.method, other.accept)
					assert.Equal(t, fmt.Sprint(window), response.Header().Get("Retry-After"))
					assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				}
			})
		}
	}
}
