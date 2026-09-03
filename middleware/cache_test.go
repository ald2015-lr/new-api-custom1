package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCacheHeadersByPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.NoRoute(Cache(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	testCases := []struct {
		path         string
		cacheControl string
		etag         string
	}{
		{path: "/", cacheControl: "no-cache"},
		{path: "/?utm_source=x", cacheControl: "no-cache"},
		{path: "/static/js/index.abc123.js", cacheControl: "public, max-age=31536000, immutable", etag: `"static/js/index.abc123.js"`},
		{path: "/static/css/index.abc123.css?v=1", cacheControl: "public, max-age=31536000, immutable", etag: `"static/css/index.abc123.css"`},
		{path: "/logo.png", cacheControl: "max-age=604800"},
		{path: "/console/token", cacheControl: "max-age=604800"},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			assert.Equal(t, tc.cacheControl, recorder.Header().Get("Cache-Control"))
			assert.Equal(t, tc.etag, recorder.Header().Get("ETag"))
			assert.NotEmpty(t, recorder.Header().Get("Cache-Version"))
		})
	}
}
