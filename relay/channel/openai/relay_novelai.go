package openai

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// novelAIResponseLimit bounds what is kept of a NovelAI response to count its
// images or to convert it, and what a zip may expand to while it is
// converted. A longer response streams through as sent and keeps the reserved
// quantity. Tests lower it.
var novelAIResponseLimit = 64 << 20

// The two formats a NovelAI generate-image response comes in: a zip of PNGs,
// or JSON carrying them base64 encoded.
const (
	novelAIFormatZip  = "zip"
	novelAIFormatJSON = "json"
)

// novelAIZipMagic starts a zip archive (its first local file header).
var novelAIZipMagic = []byte("PK\x03\x04")

// errNovelAIArchiveTooLarge fails the conversion of a zip whose PNG entries
// expand beyond novelAIResponseLimit.
var errNovelAIArchiveTooLarge = errors.New("the archive expands beyond the response limit")

const novelAIWriteChunk = 32 << 10

// NovelAIImageHandler relays a successful NovelAI generate-image response.
//
// Response format: /ai/generate-image answers as NovelAI does, with a zip of
// the PNGs unless the client's Accept names application/json, in which case
// with JSON {"images":[{"image":"<base64>"}, ...]}. The client's Accept goes
// upstream unchanged, so an upstream that honours it answers in that format,
// and then the response streams through as sent: the upstream status,
// Content-Type, Content-Disposition, Content-Encoding and, when known,
// Content-Length, then the body as it arrives, flushed on every read. The
// first bytes and the Content-Type tell the upstream format: a zip starts with
// PK\x03\x04 or is typed application/zip or application/x-zip-compressed;
// JSON is typed application/json or +json or starts with "{". Only a response
// in the other format is held whole (within novelAIResponseLimit) and
// converted, keeping the upstream status with its own Content-Type and
// Content-Length: the JSON images become the stored entries image_0.png,
// image_1.png, ... of a zip typed application/x-zip-compressed, a type NovelAI
// clients read as a zip, and the .png entries of a zip become the JSON images
// in archive order. A response that cannot be converted (no image, more
// images than dto.MaxImageN, invalid base64, a corrupt zip, more than the
// limit, expanded or not) goes out as sent, as does an encoded one and one of
// neither format.
// /ai/generate-image-stream is never converted.
//
// Billing: once upstream finished the body, the billed quantity follows the
// images in the body the client received (novelAIImageResponseCount). A
// client that leaves, an upstream read error after the first byte reached the
// client, or a body over the limit (a zip that expands past it included)
// keeps the reserved quantity. An upstream read error before any byte reached
// the client fails the attempt, unless upstream had started the body and the
// client left first: the images exist then, and a client abort must not lower
// the charge (novelAIReadFailed).
//
// As on the other relay streams, an upstream that sends nothing for
// STREAMING_TIMEOUT counts as such a read error, and each client write runs
// under the stream write deadline, so a client that stops reading fails it.
func NovelAIImageHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	idleTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	if idleTimeout <= 0 {
		// STREAMING_TIMEOUT unset: no idle limit.
		idleTimeout = math.MaxInt64
	}
	body := &novelAIResponseReader{body: resp.Body, timeout: idleTimeout}
	body.idle = time.AfterFunc(idleTimeout, func() {
		body.stalled.Store(true)
		_ = resp.Body.Close()
	})
	body.idle.Stop()
	defer body.idle.Stop()

	written := 0
	forward := func(p []byte) error {
		for len(p) > 0 {
			n := min(len(p), novelAIWriteChunk)
			helper.ExtendWriteDeadline(c)
			if _, err := c.Writer.Write(p[:n]); err != nil {
				return err
			}
			c.Writer.Flush()
			written += n
			p = p[n:]
		}
		return nil
	}
	chunk := make([]byte, novelAIWriteChunk)

	// held is what the client has not received yet: the first bytes, which
	// tell the upstream format, and when that is not the wanted one the whole
	// response, to convert it.
	var held []byte
	ended := false
	// Cleared for a body too large to count: the reserved quantity stays.
	countable := true
	encoding := resp.Header.Get("Content-Encoding")
	if info.RelayMode == relayconstant.RelayModeNovelAIGenerateImage && (encoding == "" || strings.EqualFold(encoding, "identity")) {
		wanted := novelAIFormatZip
		if strings.Contains(strings.ToLower(strings.Join(c.Request.Header.Values("Accept"), ",")), "application/json") {
			wanted = novelAIFormatJSON
		}
		for !ended && len(held) <= novelAIResponseLimit {
			if len(held) >= len(novelAIZipMagic) && len(bytes.TrimLeft(held, " \t\r\n")) > 0 {
				break
			}
			n, err := body.Read(chunk)
			held = append(held, chunk[:n]...)
			ended = errors.Is(err, io.EOF)
			if err != nil && !ended {
				return novelAIReadFailed(c, info, len(held) > 0, err)
			}
		}
		format := novelAIResponseFormat(resp.Header.Get("Content-Type"), held)
		if format != "" && format != wanted && len(held) <= novelAIResponseLimit {
			if !ended {
				rest, err := io.ReadAll(io.LimitReader(body, int64(novelAIResponseLimit-len(held)+1)))
				if err != nil {
					return novelAIReadFailed(c, info, len(held) > 0, err)
				}
				held = append(held, rest...)
				// Past the limit the response streams on as sent.
				ended = len(held) <= novelAIResponseLimit
			}
			if ended {
				converted, contentType, err := convertNovelAIResponse(held, wanted)
				if err == nil {
					c.Writer.Header().Set("Content-Type", contentType)
					c.Writer.Header().Set("Content-Length", strconv.Itoa(len(converted)))
					info.SetFirstResponseTime()
					c.Writer.WriteHeader(resp.StatusCode)
					if err := forward(converted); err != nil {
						logger.LogWarn(c, common.LogText("NovelAI response stopped after %d bytes, billing the reserved %d images: %s", written, info.RequestedImageCount(), err.Error()))
						return &dto.Usage{}, nil
					}
					info.UpdateImageCount(novelAIImageResponseCount(contentType, converted))
					return &dto.Usage{}, nil
				}
				logger.LogWarn(c, common.LogText("NovelAI response could not be converted to %s, sending it as received: %s", wanted, err.Error()))
				countable = !errors.Is(err, errNovelAIArchiveTooLarge)
			}
		}
	}

	// As sent: what was held back, then the rest as it arrives.
	counted := new(bytes.Buffer)
	countable = countable && len(held) <= novelAIResponseLimit
	if countable {
		counted = bytes.NewBuffer(held)
	}
	data := held
	var readErr error
	if ended {
		readErr = io.EOF
	}
	for {
		if len(data) > 0 {
			if written == 0 {
				info.SetFirstResponseTime()
				writeNovelAIResponseHeader(c, resp)
			}
			if err := forward(data); err != nil {
				logger.LogWarn(c, common.LogText("NovelAI response stopped after %d bytes, billing the reserved %d images: %s", written, info.RequestedImageCount(), err.Error()))
				return &dto.Usage{}, nil
			}
		}
		if readErr != nil {
			break
		}
		var n int
		n, readErr = body.Read(chunk)
		data = chunk[:n]
		if countable && counted.Len()+n > novelAIResponseLimit {
			countable = false
			counted = new(bytes.Buffer)
		}
		if countable {
			counted.Write(data)
		}
	}
	if !errors.Is(readErr, io.EOF) {
		if written == 0 {
			// Every byte read is forwarded, so nothing of the body arrived.
			return novelAIReadFailed(c, info, false, readErr)
		}
		logger.LogWarn(c, common.LogText("NovelAI response stopped after %d bytes, billing the reserved %d images: %s", written, info.RequestedImageCount(), readErr.Error()))
		return &dto.Usage{}, nil
	}
	if written == 0 {
		writeNovelAIResponseHeader(c, resp)
		c.Writer.WriteHeaderNow()
	}
	if countable {
		info.UpdateImageCount(novelAIImageResponseCount(resp.Header.Get("Content-Type"), counted.Bytes()))
	}
	return &dto.Usage{}, nil
}

// novelAIReadFailed settles an upstream read that failed before any byte
// reached the client. Once upstream has started the body, a client that left
// first (its request context is done, which also ends the upstream read) keeps
// the reserved quantity: the images exist, and a client abort must not lower
// the charge. Any other failure fails the attempt, which charges nothing. That
// includes a client, or a proxy in front of new-api, giving up on an upstream
// that answered 2xx but sent no body: a stall is the upstream's fault, and a
// stream endpoint opens with its headers before it generates anything.
func novelAIReadFailed(c *gin.Context, info *relaycommon.RelayInfo, bodyStarted bool, err error) (*dto.Usage, *types.NewAPIError) {
	if bodyStarted && c.Request.Context().Err() != nil {
		logger.LogWarn(c, common.LogText("NovelAI client left before the response reached it, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
		return &dto.Usage{}, nil
	}
	return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
}

// novelAIResponseReader reads a NovelAI response body and fails a read once
// upstream sent nothing for the idle timeout. The timer runs only while a
// read waits on upstream; when it fires it closes the body, which ends that
// read.
type novelAIResponseReader struct {
	body    io.Reader
	timeout time.Duration
	idle    *time.Timer
	stalled atomic.Bool
}

func (r *novelAIResponseReader) Read(p []byte) (int, error) {
	r.idle.Reset(r.timeout)
	n, err := r.body.Read(p)
	r.idle.Stop()
	if err != nil && !errors.Is(err, io.EOF) && r.stalled.Load() {
		err = fmt.Errorf("upstream sent nothing for %s", r.timeout)
	}
	return n, err
}

// writeNovelAIResponseHeader forwards the upstream status and the headers that
// describe the body. Content-Length is known unless the transport decoded a
// compressed body, in which case it dropped the length and the encoding.
func writeNovelAIResponseHeader(c *gin.Context, resp *http.Response) {
	for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Encoding"} {
		if value := resp.Header.Get(name); value != "" {
			c.Writer.Header().Set(name, value)
		}
	}
	if resp.ContentLength >= 0 {
		c.Writer.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	c.Writer.WriteHeader(resp.StatusCode)
}

// novelAIResponseFormat tells the format of a NovelAI response from its
// Content-Type and first bytes: novelAIFormatZip, novelAIFormatJSON, or ""
// for neither (msgpack stream events, for one).
func novelAIResponseFormat(contentType string, prefix []byte) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch {
	case bytes.HasPrefix(prefix, novelAIZipMagic), mediaType == "application/zip", mediaType == "application/x-zip-compressed":
		return novelAIFormatZip
	case mediaType == "application/json", strings.HasSuffix(mediaType, "+json"), bytes.HasPrefix(bytes.TrimLeft(prefix, " \t\r\n"), []byte("{")):
		return novelAIFormatJSON
	}
	return ""
}

// convertNovelAIResponse rewrites a complete NovelAI response in the format
// to and returns it with its Content-Type. JSON becomes a zip whose stored
// entries image_0.png, image_1.png, ... are the images[] entries (or the one
// object-shaped images) with a non-empty image, base64 decoded. A zip becomes
// {"images":[{"image":"<base64>"}, ...]} of its .png entries in archive
// order; the entries are read through novelAIResponseLimit in all, whatever
// sizes the archive claims, so a zip bomb fails the conversion with
// errNovelAIArchiveTooLarge. A response without images, with more than
// dto.MaxImageN, or with one that cannot be decoded, fails it as well.
func convertNovelAIResponse(body []byte, to string) ([]byte, string, error) {
	if to == novelAIFormatZip {
		if !gjson.ValidBytes(body) {
			return nil, "", errors.New("invalid JSON")
		}
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		count := 0
		var err error
		// Walked with ForEach: Array would hold a result for every entry,
		// and a response within the limit can list millions.
		store := func(_, item gjson.Result) bool {
			if !openaiImageDataHasField(item, "image") {
				return true
			}
			if count == dto.MaxImageN {
				// No request asks for more, and billing keeps the
				// reservation for a larger count, so it is not worth
				// converting.
				err = fmt.Errorf("more than %d images", dto.MaxImageN)
				return false
			}
			png, decodeErr := base64.StdEncoding.DecodeString(item.Get("image").String())
			if decodeErr != nil || len(png) == 0 {
				err = fmt.Errorf("image %d is not base64 image data", count)
				return false
			}
			// Raw, with the CRC and sizes in the local header: CreateHeader
			// would put them in a data descriptor after the entry, which
			// streaming readers such as Java's ZipInputStream reject on a
			// stored entry.
			var entry io.Writer
			entry, err = writer.CreateRaw(&zip.FileHeader{
				Name:               fmt.Sprintf("image_%d.png", count),
				Method:             zip.Store,
				CRC32:              crc32.ChecksumIEEE(png),
				CompressedSize64:   uint64(len(png)),
				UncompressedSize64: uint64(len(png)),
				ReaderVersion:      20,
				CreatorVersion:     20,
			})
			if err == nil {
				_, err = entry.Write(png)
			}
			count++
			return err == nil
		}
		images := gjson.GetBytes(body, "images")
		if images.IsObject() {
			// ForEach would visit its fields: an object is the one image.
			store(gjson.Result{}, images)
		} else if images.IsArray() {
			images.ForEach(store)
		}
		if err != nil {
			return nil, "", err
		}
		if count == 0 {
			return nil, "", errors.New("no images")
		}
		if err = writer.Close(); err != nil {
			return nil, "", err
		}
		// A type NovelAI clients take for a zip: novelai-api, for one, reads
		// one only as application/x-zip-compressed or binary/octet-stream and
		// fails on application/zip.
		return archive.Bytes(), "application/x-zip-compressed", nil
	}

	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, "", err
	}
	var images []map[string]string
	// One read budget across the entries: a byte past the limit fails it.
	budget := &io.LimitedReader{N: int64(novelAIResponseLimit) + 1}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(file.Name), ".png") {
			continue
		}
		if len(images) == dto.MaxImageN {
			return nil, "", fmt.Errorf("more than %d PNG entries", dto.MaxImageN)
		}
		entry, err := file.Open()
		if err != nil {
			return nil, "", err
		}
		budget.R = entry
		png, err := io.ReadAll(budget)
		_ = entry.Close()
		if err != nil {
			return nil, "", err
		}
		if budget.N == 0 {
			return nil, "", errNovelAIArchiveTooLarge
		}
		images = append(images, map[string]string{"image": base64.StdEncoding.EncodeToString(png)})
	}
	if len(images) == 0 {
		return nil, "", errors.New("no PNG entries")
	}
	converted, err := common.Marshal(map[string]any{"images": images})
	if err != nil {
		return nil, "", err
	}
	return converted, "application/json", nil
}

// novelAIImageResponseCount counts the images of a complete NovelAI response:
// the .png entries of a zip archive, or in a JSON body the images[] entries
// whose image is a non-empty string (an object-shaped images is one image when
// its image is non-empty). The count comes from image payloads, never from the
// array length. Anything else, such as msgpack or SSE stream events, counts
// zero, which keeps the reserved quantity.
func novelAIImageResponseCount(contentType string, body []byte) int64 {
	if novelAIResponseFormat(contentType, body) == novelAIFormatZip {
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return 0
		}
		var count int64
		for _, file := range archive.File {
			if !file.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(file.Name), ".png") {
				count++
			}
		}
		return count
	}
	if !gjson.ValidBytes(body) {
		return 0
	}
	images := gjson.GetBytes(body, "images")
	if images.IsObject() {
		if openaiImageDataHasField(images, "image") {
			return 1
		}
		return 0
	}
	var count int64
	if images.IsArray() {
		images.ForEach(func(_, item gjson.Result) bool {
			if openaiImageDataHasField(item, "image") {
				count++
			}
			return true
		})
	}
	return count
}
