package middleware

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	ginGzip "github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// precompressedFile is the gzip encoding of one frontend build file.
type precompressedFile struct {
	once        sync.Once
	body        []byte
	contentType string
	err         error
}

// brotliFile records whether the build has a brotli encoding of a file next
// to it (<file>.br) and its size. Its bytes are read from the build on each
// request instead of being held in memory.
type brotliFile struct {
	once sync.Once
	ok   bool
	size int64
}

// ServeFrontendFiles serves files from the embedded frontend build. The build
// never changes while the process runs. A client that accepts brotli gets the
// <file>.br the web build wrote next to a file, when there is one. Otherwise a
// client that accepts gzip gets the file gzip-compressed once and reused,
// instead of recompressing multi-megabyte chunks on every request; the cache
// holds at most one entry per build file. Clients without either, the image
// types the gzip middleware skips, the .br files themselves, directories, and
// the index.html redirect keep using http.FileServer.
//
// Every encoding of a file has its own entity tag (see
// encodedRepresentationETag) and every response of a compressible file
// carries Vary: Accept-Encoding, so caches never hand one client bytes in an
// encoding it did not accept.
func ServeFrontendFiles(frontendFS static.ServeFileSystem) gin.HandlerFunc {
	fileServer := http.FileServer(frontendFS)
	var gzipCache sync.Map   // URL path -> *precompressedFile
	var brotliCache sync.Map // URL path -> *brotliFile
	return func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if !frontendFS.Exists("/", urlPath) {
			return
		}
		c.Abort()
		if ginGzip.DefaultExcludedExtentions.Contains(path.Ext(urlPath)) ||
			path.Ext(urlPath) == ".br" ||
			strings.HasSuffix(urlPath, "/index.html") {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// The bytes of this file depend on Accept-Encoding, the uncompressed
		// ones included: without Vary a shared cache could keep the
		// uncompressed copy for everyone, or a compressed one for clients that
		// cannot decode it.
		c.Header("Vary", "Accept-Encoding")
		acceptEncoding := c.GetHeader("Accept-Encoding")
		acceptsGzip := strings.Contains(acceptEncoding, "gzip")
		acceptsBrotli := acceptsContentCoding(acceptEncoding, "br")
		if acceptsBrotli && serveBrotliFile(c, frontendFS, &brotliCache, urlPath, acceptsGzip) {
			return
		}
		if !acceptsGzip {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		value, ok := gzipCache.Load(urlPath)
		if !ok {
			value, _ = gzipCache.LoadOrStore(urlPath, &precompressedFile{})
		}
		file := value.(*precompressedFile)
		file.once.Do(func() {
			f, err := frontendFS.Open(urlPath)
			if err != nil {
				file.err = err
				return
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				file.err = err
				return
			}
			if info.IsDir() {
				file.err = errors.New("directory")
				return
			}
			raw, err := io.ReadAll(f)
			if err != nil {
				file.err = err
				return
			}
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(raw); err != nil {
				file.err = err
				return
			}
			if err := writer.Close(); err != nil {
				file.err = err
				return
			}
			// Match http.FileServer: extension first, then content sniffing.
			file.contentType = mime.TypeByExtension(path.Ext(urlPath))
			if file.contentType == "" {
				file.contentType = http.DetectContentType(raw)
			}
			file.body = bytes.Clone(compressed.Bytes())
		})
		if file.err != nil {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		// Range requests get the whole body; a server may ignore Range.
		if writeNotModified(c, "gzip", acceptsGzip, acceptsBrotli) {
			return
		}
		header := c.Writer.Header()
		header.Set("Content-Encoding", "gzip")
		header.Set("Content-Type", file.contentType)
		header.Set("Content-Length", strconv.Itoa(len(file.body)))
		c.Writer.WriteHeader(http.StatusOK)
		if c.Request.Method != http.MethodHead {
			_, _ = c.Writer.Write(file.body)
		}
	}
}

// serveBrotliFile answers with <urlPath>.br when the build has it and the
// file's type is known from its extension. It reports false, having written
// nothing, when there is no brotli encoding to send.
func serveBrotliFile(c *gin.Context, frontendFS static.ServeFileSystem, cache *sync.Map, urlPath string, acceptsGzip bool) bool {
	contentType := mime.TypeByExtension(path.Ext(urlPath))
	if contentType == "" {
		return false
	}
	value, ok := cache.Load(urlPath)
	if !ok {
		value, _ = cache.LoadOrStore(urlPath, &brotliFile{})
	}
	entry := value.(*brotliFile)
	entry.once.Do(func() {
		f, err := frontendFS.Open(urlPath + ".br")
		if err != nil {
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			return
		}
		entry.ok, entry.size = true, info.Size()
	})
	if !entry.ok {
		return false
	}
	f, err := frontendFS.Open(urlPath + ".br")
	if err != nil {
		return false
	}
	defer f.Close()

	// Range requests get the whole body, as on the gzip path.
	if writeNotModified(c, "br", acceptsGzip, true) {
		return true
	}
	header := c.Writer.Header()
	header.Set("Content-Encoding", "br")
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.FormatInt(entry.size, 10))
	c.Writer.WriteHeader(http.StatusOK)
	if c.Request.Method != http.MethodHead {
		_, _ = io.Copy(c.Writer, f)
	}
	return true
}

// writeNotModified sets the entity tag of the encoding about to be sent and,
// when If-None-Match names a representation of the file the client can
// decode, answers 304 with that representation's tag instead and reports
// true. Cache() gives hashed build files the tag of their uncompressed bytes,
// which every client can decode; servers before per-encoding tags sent it
// with gzip bodies too, so those revalidate without a new download. Files
// without a tag are always sent.
func writeNotModified(c *gin.Context, coding string, acceptsGzip, acceptsBrotli bool) bool {
	header := c.Writer.Header()
	identity := header.Get("ETag")
	if identity == "" {
		return false
	}
	selected := encodedRepresentationETag(identity, coding)
	header.Set("ETag", selected)
	ifNoneMatch := c.GetHeader("If-None-Match")
	if ifNoneMatch == "" {
		return false
	}
	matched := ""
	if strings.TrimSpace(ifNoneMatch) == "*" {
		matched = selected
	} else {
		decodable := []string{identity}
		if acceptsGzip {
			decodable = append(decodable, encodedRepresentationETag(identity, "gzip"))
		}
		if acceptsBrotli {
			decodable = append(decodable, encodedRepresentationETag(identity, "br"))
		}
		for _, candidate := range decodable {
			if etagMatches(candidate, ifNoneMatch) {
				matched = candidate
				break
			}
		}
	}
	if matched == "" {
		return false
	}
	header.Set("ETag", matched)
	c.Writer.WriteHeader(http.StatusNotModified)
	c.Writer.WriteHeaderNow()
	return true
}

// encodedRepresentationETag is the entity tag of a file's bytes in a content
// coding, derived from the tag of its uncompressed bytes: "<tag>-gzip",
// "<tag>-br". Each encoding is a different representation and needs its own
// strong tag.
func encodedRepresentationETag(identity string, coding string) string {
	weak := strings.HasPrefix(identity, "W/")
	opaque := strings.TrimPrefix(identity, "W/")
	if len(opaque) < 2 || !strings.HasPrefix(opaque, `"`) || !strings.HasSuffix(opaque, `"`) {
		return identity
	}
	tag := opaque[:len(opaque)-1] + "-" + coding + `"`
	if weak {
		return "W/" + tag
	}
	return tag
}

// acceptsContentCoding reports whether an Accept-Encoding header accepts
// coding: listed with a q-value above 0, or not listed while "*" is.
func acceptsContentCoding(acceptEncoding string, coding string) bool {
	wildcard := false
	for _, item := range strings.Split(acceptEncoding, ",") {
		name, params, _ := strings.Cut(item, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != coding && name != "*" {
			continue
		}
		accepted := true
		for _, param := range strings.Split(params, ";") {
			key, value, found := strings.Cut(param, "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				accepted = err == nil && q > 0
			}
		}
		if name == coding {
			return accepted
		}
		wildcard = accepted
	}
	return wildcard
}

// etagMatches reports whether an If-None-Match header lists etag, using the
// weak comparison RFC 9110 prescribes for If-None-Match.
func etagMatches(etag string, ifNoneMatch string) bool {
	if etag == "" || ifNoneMatch == "" {
		return false
	}
	if strings.TrimSpace(ifNoneMatch) == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == want {
			return true
		}
	}
	return false
}
