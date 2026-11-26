package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingFrontendFS counts the opens of one file (target) and of all files.
type countingFrontendFS struct {
	static.ServeFileSystem
	target string
	opens  atomic.Int32
	all    atomic.Int32
}

func (f *countingFrontendFS) Open(name string) (http.File, error) {
	f.all.Add(1)
	if name == f.target {
		f.opens.Add(1)
	}
	return f.ServeFileSystem.Open(name)
}

func performFrontendRequest(router http.Handler, method, target, acceptEncoding string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, nil)
	if acceptEncoding != "" {
		request.Header.Set("Accept-Encoding", acceptEncoding)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestServeFrontendFilesCompressesEachFileOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontendDir := t.TempDir()
	script := []byte(strings.Repeat("export const answer = 42;\n", 400))
	image := []byte("\x89PNG\r\n\x1a\nnot-really-an-image")
	require.NoError(t, os.MkdirAll(filepath.Join(frontendDir, "static", "js"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "static", "js", "app.js"), script, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "logo.png"), image, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte("<html></html>"), 0o644))
	frontendFS := &countingFrontendFS{ServeFileSystem: static.LocalFile(frontendDir, false), target: "/static/js/app.js"}

	router := gin.New()
	router.NoRoute(ServeFrontendFiles(frontendFS), func(c *gin.Context) {
		c.String(http.StatusTeapot, "fallback")
	})

	plain := performFrontendRequest(router, http.MethodGet, "/static/js/app.js", "")
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Header().Get("Content-Encoding"))
	assert.Equal(t, script, plain.Body.Bytes())
	frontendFS.opens.Store(0)
	frontendFS.all.Store(0)

	responses := make([]*httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Go(func() {
			responses[i] = performFrontendRequest(router, http.MethodGet, "/static/js/app.js", "gzip, deflate, br")
		})
	}
	wg.Wait()
	for _, response := range responses {
		require.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "gzip", response.Header().Get("Content-Encoding"))
		assert.Equal(t, "Accept-Encoding", response.Header().Get("Vary"))
		assert.Equal(t, plain.Header().Get("Content-Type"), response.Header().Get("Content-Type"))
		assert.Equal(t, strconv.Itoa(response.Body.Len()), response.Header().Get("Content-Length"))
		assert.Equal(t, responses[0].Body.Bytes(), response.Body.Bytes())
	}
	reader, err := gzip.NewReader(bytes.NewReader(responses[0].Body.Bytes()))
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, script, decoded)

	head := performFrontendRequest(router, http.MethodHead, "/static/js/app.js", "gzip")
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Equal(t, "gzip", head.Header().Get("Content-Encoding"))
	assert.Equal(t, strconv.Itoa(responses[0].Body.Len()), head.Header().Get("Content-Length"))
	assert.Empty(t, head.Body.Bytes())
	assert.Equal(t, int32(1), frontendFS.opens.Load(), "the file must be read and compressed once")
	assert.Equal(t, int32(2), frontendFS.all.Load(), "besides, one look for app.js.br, whose absence is remembered")

	png := performFrontendRequest(router, http.MethodGet, "/logo.png", "gzip")
	assert.Equal(t, http.StatusOK, png.Code)
	assert.Empty(t, png.Header().Get("Content-Encoding"))
	assert.Equal(t, image, png.Body.Bytes())

	index := performFrontendRequest(router, http.MethodGet, "/index.html", "gzip")
	assert.Equal(t, http.StatusMovedPermanently, index.Code)
	assert.Equal(t, "./", index.Header().Get("Location"))

	missing := performFrontendRequest(router, http.MethodGet, "/static/js/missing.js", "gzip")
	assert.Equal(t, http.StatusTeapot, missing.Code)
	assert.Equal(t, "fallback", missing.Body.String())
}

// Every response of a file that is served compressed carries Vary:
// Accept-Encoding, the uncompressed one included, so a shared cache keeps a
// copy per encoding. Files that are never compressed do not vary.
func TestServeFrontendFilesVaryOnAcceptEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontendDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(frontendDir, "static", "js"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "static", "js", "app.js"), []byte(strings.Repeat("let a = 1;\n", 100)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "logo.png"), []byte("\x89PNG\r\n\x1a\nnot-really-an-image"), 0o644))
	router := gin.New()
	router.NoRoute(ServeFrontendFiles(static.LocalFile(frontendDir, false)))

	for _, acceptEncoding := range []string{"", "identity", "deflate", "gzip, deflate, br"} {
		response := performFrontendRequest(router, http.MethodGet, "/static/js/app.js", acceptEncoding)
		require.Equal(t, http.StatusOK, response.Code, acceptEncoding)
		assert.Equal(t, "Accept-Encoding", response.Header().Get("Vary"), "Accept-Encoding %q", acceptEncoding)
	}
	head := performFrontendRequest(router, http.MethodHead, "/static/js/app.js", "")
	assert.Equal(t, "Accept-Encoding", head.Header().Get("Vary"))
	assert.Empty(t, head.Header().Get("Content-Encoding"))

	png := performFrontendRequest(router, http.MethodGet, "/logo.png", "gzip")
	assert.Equal(t, http.StatusOK, png.Code)
	assert.Empty(t, png.Header().Get("Vary"))
}

// writeBrotliFile writes name and, next to it, name.br as the web build does.
func writeBrotliFile(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	require.NoError(t, os.WriteFile(name, content, 0o644))
	var encoded bytes.Buffer
	writer := brotli.NewWriterLevel(&encoded, brotli.BestCompression)
	_, err := writer.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(name+".br", encoded.Bytes(), 0o644))
	return encoded.Bytes()
}

// A client that accepts brotli gets the <file>.br written next to a build
// file, with its own Content-Length and entity tag; a client without brotli,
// or a file without a .br, keeps gzip or the uncompressed bytes. Every
// response varies on Accept-Encoding, and a revalidation is answered 304 when
// If-None-Match names a representation the client can decode.
func TestServeFrontendFilesServesPrecompressedBrotli(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontendDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(frontendDir, "static", "js"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(frontendDir, "static", "css"), 0o755))
	script := []byte(strings.Repeat("export const answer = 42;\n", 400))
	brotliScript := writeBrotliFile(t, filepath.Join(frontendDir, "static", "js", "app.js"), script)
	style := []byte(strings.Repeat("body { margin: 0 }\n", 200))
	require.NoError(t, os.WriteFile(filepath.Join(frontendDir, "static", "css", "site.css"), style, 0o644))
	router := gin.New()
	router.NoRoute(Cache(), ServeFrontendFiles(static.LocalFile(frontendDir, false)), func(c *gin.Context) {
		c.String(http.StatusTeapot, "fallback")
	})
	request := func(method, target, acceptEncoding, ifNoneMatch string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, nil)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	const (
		identityTag = `"static/js/app.js"`
		gzipTag     = `"static/js/app.js-gzip"`
		brotliTag   = `"static/js/app.js-br"`
	)

	for _, acceptEncoding := range []string{"gzip, deflate, br", "br", "gzip;q=0.5, br;q=1", "*"} {
		response := request(http.MethodGet, "/static/js/app.js", acceptEncoding, "")
		require.Equal(t, http.StatusOK, response.Code, acceptEncoding)
		assert.Equal(t, "br", response.Header().Get("Content-Encoding"), acceptEncoding)
		assert.Equal(t, "Accept-Encoding", response.Header().Get("Vary"))
		assert.Equal(t, brotliTag, response.Header().Get("ETag"))
		assert.Equal(t, "text/javascript; charset=utf-8", response.Header().Get("Content-Type"))
		assert.Equal(t, strconv.Itoa(len(brotliScript)), response.Header().Get("Content-Length"))
		assert.Equal(t, "public, max-age=31536000, immutable", response.Header().Get("Cache-Control"))
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(response.Body.Bytes())))
		require.NoError(t, err)
		assert.Equal(t, script, decoded)
	}
	head := request(http.MethodHead, "/static/js/app.js", "br", "")
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Equal(t, "br", head.Header().Get("Content-Encoding"))
	assert.Equal(t, strconv.Itoa(len(brotliScript)), head.Header().Get("Content-Length"))
	assert.Empty(t, head.Body.Bytes())

	for _, acceptEncoding := range []string{"gzip, deflate", "gzip, br;q=0"} {
		gzipped := request(http.MethodGet, "/static/js/app.js", acceptEncoding, "")
		require.Equal(t, http.StatusOK, gzipped.Code)
		assert.Equal(t, "gzip", gzipped.Header().Get("Content-Encoding"), acceptEncoding)
		assert.Equal(t, gzipTag, gzipped.Header().Get("ETag"))
		assert.Equal(t, "Accept-Encoding", gzipped.Header().Get("Vary"))
		assert.Equal(t, strconv.Itoa(gzipped.Body.Len()), gzipped.Header().Get("Content-Length"))
	}
	plain := request(http.MethodGet, "/static/js/app.js", "", "")
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Header().Get("Content-Encoding"))
	assert.Equal(t, identityTag, plain.Header().Get("ETag"))
	assert.Equal(t, "Accept-Encoding", plain.Header().Get("Vary"))
	assert.Equal(t, script, plain.Body.Bytes())

	// A file without a .br keeps gzip, or its bytes when gzip is not accepted.
	css := request(http.MethodGet, "/static/css/site.css", "gzip, deflate, br", "")
	assert.Equal(t, "gzip", css.Header().Get("Content-Encoding"))
	cssPlain := request(http.MethodGet, "/static/css/site.css", "br", "")
	assert.Empty(t, cssPlain.Header().Get("Content-Encoding"))
	assert.Equal(t, style, cssPlain.Body.Bytes())
	assert.Equal(t, "Accept-Encoding", cssPlain.Header().Get("Vary"))

	// The .br file itself is served as it is.
	raw := request(http.MethodGet, "/static/js/app.js.br", "gzip, deflate, br", "")
	assert.Equal(t, http.StatusOK, raw.Code)
	assert.Empty(t, raw.Header().Get("Content-Encoding"))
	assert.Equal(t, brotliScript, raw.Body.Bytes())

	for _, tc := range []struct {
		name, acceptEncoding, ifNoneMatch string
		status                            int
		etag                              string
	}{
		{"brotli copy, brotli client", "gzip, deflate, br", brotliTag, http.StatusNotModified, brotliTag},
		{"gzip copy, brotli client", "gzip, deflate, br", gzipTag, http.StatusNotModified, gzipTag},
		{"uncompressed or older gzip copy, brotli client", "gzip, deflate, br", `W/"other", ` + identityTag, http.StatusNotModified, identityTag},
		{"any copy, brotli client", "gzip, deflate, br", "*", http.StatusNotModified, brotliTag},
		{"brotli copy, gzip client", "gzip, deflate", brotliTag, http.StatusOK, gzipTag},
		{"gzip copy, gzip client", "gzip, deflate", gzipTag, http.StatusNotModified, gzipTag},
		{"gzip copy, client without compression", "", gzipTag, http.StatusOK, identityTag},
		{"brotli copy, client without compression", "", brotliTag, http.StatusOK, identityTag},
		{"uncompressed copy, client without compression", "", identityTag, http.StatusNotModified, identityTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(http.MethodGet, "/static/js/app.js", tc.acceptEncoding, tc.ifNoneMatch)
			assert.Equal(t, tc.status, response.Code)
			assert.Equal(t, tc.etag, response.Header().Get("ETag"))
			assert.Equal(t, "Accept-Encoding", response.Header().Get("Vary"))
			if tc.status == http.StatusNotModified {
				assert.Empty(t, response.Body.Bytes())
				assert.Empty(t, response.Header().Get("Content-Encoding"))
			}
		})
	}
}

func TestAcceptsContentCoding(t *testing.T) {
	for header, want := range map[string]bool{
		"":                      false,
		"gzip, deflate":         false,
		"gzip, deflate, br":     true,
		"BR":                    true,
		"br;q=0":                false,
		"br; q=0.0":             false,
		"br;q=0.001":            true,
		"gzip, *":               true,
		"*;q=0":                 false,
		"br;q=0, *":             false,
		"gzip;q=1.0, br;q=0.8 ": true,
		"brotli":                false,
	} {
		assert.Equal(t, want, acceptsContentCoding(header, "br"), "Accept-Encoding %q", header)
	}
}
