package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		switch {
		case strings.HasPrefix(requestPath, "/static/"):
			// Rsbuild emits content-hashed filenames under /static, so the
			// path identifies the bytes: cache for a year and give net/http
			// an ETag so a revalidation gets a 304 instead of the full file.
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
			c.Header("ETag", `"`+strings.TrimPrefix(requestPath, "/")+`"`)
			c.Header("X-Content-Type-Options", "nosniff")
		case requestPath == "/":
			c.Header("Cache-Control", "no-cache")
		default:
			c.Header("Cache-Control", "max-age=604800") // one week
		}
		c.Header("Cache-Version", "b688f2fb5be447c25e5aa3bd063087a83db32a288bf6a4f35f2d8db310e40b14")
		c.Next()
	}
}
