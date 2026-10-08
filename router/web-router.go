package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

const defaultWebDistPath = "web/dist"

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
	// DistPath is the directory inside BuildFS that holds the built
	// frontend; it defaults to web/dist and only tests override it.
	DistPath string
}

func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	distPath := assets.DistPath
	if distPath == "" {
		distPath = defaultWebDistPath
	}
	frontendFS := common.EmbedFolder(assets.BuildFS, distPath)
	compress := gzip.Gzip(gzip.DefaultCompression)

	router.NoRoute(
		pluginDispatcher,
		middleware.RouteTag("web"),
		func(c *gin.Context) {
			// ServeFrontendFiles sends build files already compressed.
			if !frontendFS.Exists("/", c.Request.URL.Path) {
				compress(c)
			}
		},
		middleware.AccessTokenAudit(),
		// Build files get their own (by default disabled) limiter, so only the
		// SPA HTML fallback and 404s count against GLOBAL_WEB_RATE_LIMIT. A cold
		// load needs dozens of hashed chunks; counting them made the per-IP
		// budget run out mid-navigation and the 429 on a route chunk surfaced
		// as the dashboard's generic error page.
		middleware.GlobalWebRateLimit(frontendFS),
		middleware.Cache(),
		middleware.ServeFrontendFiles(frontendFS),
		func(c *gin.Context) {
			requestPath := c.Request.URL.Path
			if strings.HasPrefix(requestPath, "/static/") {
				// A hashed chunk that no longer exists (a tab left open across
				// a deploy) must fail the import loudly instead of handing
				// index.html to a <script> tag.
				c.Header("Cache-Control", "no-store")
				c.String(http.StatusNotFound, "404 page not found")
				return
			}
			if strings.HasPrefix(requestPath, "/v1") || strings.HasPrefix(requestPath, "/api") || strings.HasPrefix(requestPath, "/assets") {
				c.Header("Cache-Control", "no-store")
				controller.RelayNotFound(c)
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
		},
	)
}
