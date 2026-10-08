package router

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/dist
var webRouterTestFS embed.FS

const webRouterTestIndex = "<!doctype html><html><body>spa</body></html>"

func newWebRouterTest(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies(nil))
	SetWebRouter(engine, WebAssets{
		BuildFS:   webRouterTestFS,
		IndexPage: []byte(webRouterTestIndex),
		DistPath:  "testdata/dist",
	}, func(c *gin.Context) { c.Next() })
	return engine
}

func performWebRequest(handler http.Handler, path string, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remoteAddr
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestWebRouterServesHashedAssetsAsImmutableWithETag(t *testing.T) {
	engine := newWebRouterTest(t)

	response := performWebRequest(engine, "/static/js/app.js", "192.0.2.10:1234", nil)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Header().Get("Content-Type"), "javascript")
	assert.Equal(t, "public, max-age=31536000, immutable", response.Header().Get("Cache-Control"))
	assert.Equal(t, `"static/js/app.js"`, response.Header().Get("ETag"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "console.log(\"app\");\n", response.Body.String())

	revalidated := performWebRequest(engine, "/static/js/app.js", "192.0.2.10:1234", map[string]string{
		"If-None-Match": `"static/js/app.js"`,
	})
	assert.Equal(t, http.StatusNotModified, revalidated.Code)
	assert.Empty(t, revalidated.Body.String())

	// Browsers send Accept-Encoding: gzip, which takes the precompressed path.
	gzipped := performWebRequest(engine, "/static/js/app.js", "192.0.2.10:1234", map[string]string{
		"Accept-Encoding": "gzip, deflate, br",
	})
	require.Equal(t, http.StatusOK, gzipped.Code)
	assert.Equal(t, "gzip", gzipped.Header().Get("Content-Encoding"))
	assert.Equal(t, `"static/js/app.js"`, gzipped.Header().Get("ETag"))
	gzipRevalidated := performWebRequest(engine, "/static/js/app.js", "192.0.2.10:1234", map[string]string{
		"Accept-Encoding": "gzip, deflate, br",
		"If-None-Match":   `W/"other", "static/js/app.js"`,
	})
	assert.Equal(t, http.StatusNotModified, gzipRevalidated.Code)
	assert.Empty(t, gzipRevalidated.Body.String())
	assert.Empty(t, gzipRevalidated.Header().Get("Content-Encoding"))
}

func TestWebRouterMissingChunkIs404NotIndexHTML(t *testing.T) {
	engine := newWebRouterTest(t)

	response := performWebRequest(engine, "/static/js/async/1234.deadbeef.js", "192.0.2.11:1234", nil)
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.NotContains(t, response.Body.String(), "<html")
	assert.NotContains(t, response.Header().Get("Content-Type"), "text/html")
}

func TestWebRouterAPIMissesAreJSON404WithoutCache(t *testing.T) {
	engine := newWebRouterTest(t)

	for _, path := range []string{"/api/nope", "/v1/nope", "/assets/old.js"} {
		response := performWebRequest(engine, path, "192.0.2.12:1234", nil)
		assert.Equal(t, http.StatusNotFound, response.Code, path)
		assert.Contains(t, response.Header().Get("Cache-Control"), "no-store", path)
		assert.Contains(t, response.Header().Get("Content-Type"), "application/json", path)
	}
}

func TestWebRouterSPAFallbackIsNoCacheHTML(t *testing.T) {
	engine := newWebRouterTest(t)

	for _, path := range []string{"/", "/?utm=1", "/console/token", "/index.html"} {
		response := performWebRequest(engine, path, "192.0.2.13:1234", nil)
		if path == "/index.html" {
			// The embedded FS redirects /index.html to ./; the redirected
			// request is then answered like "/".
			assert.Equal(t, http.StatusMovedPermanently, response.Code, path)
			continue
		}
		assert.Equal(t, http.StatusOK, response.Code, path)
		assert.Equal(t, "no-cache", response.Header().Get("Cache-Control"), path)
		assert.Contains(t, response.Header().Get("Content-Type"), "text/html", path)
		assert.Equal(t, webRouterTestIndex, response.Body.String(), path)
	}
}

func TestWebRouterRateLimitSkipsEmbeddedAssets(t *testing.T) {
	previousEnable := common.GlobalWebRateLimitEnable
	previousNum := common.GlobalWebRateLimitNum
	previousDuration := common.GlobalWebRateLimitDuration
	previousRedis := common.RedisEnabled
	t.Cleanup(func() {
		common.GlobalWebRateLimitEnable = previousEnable
		common.GlobalWebRateLimitNum = previousNum
		common.GlobalWebRateLimitDuration = previousDuration
		common.RedisEnabled = previousRedis
	})
	common.GlobalWebRateLimitEnable = true
	common.GlobalWebRateLimitNum = 2
	common.GlobalWebRateLimitDuration = 180
	common.RedisEnabled = false

	// The limiter is built when the router is set up, so it must see the
	// values above.
	engine := newWebRouterTest(t)
	remoteAddr := "192.0.2.14:1234"

	for i := 0; i < 5; i++ {
		response := performWebRequest(engine, "/static/js/app.js", remoteAddr, nil)
		assert.Equal(t, http.StatusOK, response.Code, "asset request %d must not be rate limited", i)
	}

	assert.Equal(t, http.StatusOK, performWebRequest(engine, "/console", remoteAddr, nil).Code)
	assert.Equal(t, http.StatusOK, performWebRequest(engine, "/console/token", remoteAddr, nil).Code)
	limited := performWebRequest(engine, "/console/log", remoteAddr, nil)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, "180", limited.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", limited.Header().Get("Cache-Control"))

	// Assets keep flowing for the same client even after the page budget is spent.
	assert.Equal(t, http.StatusOK, performWebRequest(engine, "/static/js/app.js", remoteAddr, nil).Code)
}
