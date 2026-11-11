package openai

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newImageTestContext(t *testing.T, body, contentType string, isStream bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{contentType}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
		IsStream:    isStream,
	}
	return c, recorder, resp, info
}

func TestImageExpressionUsesCompletedCountAndProtectsAbortedStreams(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	for _, tc := range []struct {
		name, body      string
		stream, abort   bool
		requested, want int
	}{
		{"JSON uses actual count", `{"data":[{"b64_json":"first"},{"b64_json":"second"}]}`, false, false, 3, 2},
		{"JSON wrapped as SSE uses actual count", `{"data":[{"b64_json":"first"}]}`, true, false, 3, 1},
		{"empty response retains request", `{"data":[]}`, false, false, 3, 3},
		{"JSON object data counts one image", `{"data":{"url":"https://example.com/a.png","b64_json":"first"}}`, false, false, 3, 1},
		{"JSON wrapped as SSE counts object data once", `{"data":{"b64_json":"first"}}`, true, false, 3, 1},
		{"completed stream refunds missing images", "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"first\"}\n\ndata: [DONE]\n\n", true, false, 3, 1},
		{"client abort cannot reduce count", "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"first\"}\n\n", true, true, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contentType := "application/json"
			if strings.HasPrefix(tc.body, "data:") {
				contentType = "text/event-stream"
			}
			c, _, resp, info := newImageTestContext(t, tc.body, contentType, tc.stream)
			if tc.abort {
				c, _, resp, info = newDisconnectingImageStream(t, tc.body, "first")
			}
			info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{EstimatedImageCount: &tc.requested}
			if tc.stream {
				_, err := OpenaiImageStreamHandler(c, info, resp)
				require.Nil(t, err)
			} else {
				_, err := OpenaiImageHandler(c, info, resp)
				require.Nil(t, err)
			}
			count := info.RequestedImageCount()
			if info.BillingImageCount != nil {
				count = *info.BillingImageCount
			}
			assert.Equal(t, tc.want, count)
			assert.Empty(t, info.PriceData.OtherRatios(), "expression quantities must not add a legacy multiplier")
		})
	}
}

func TestImageCacheUsageAcrossResponseFormats(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	// Contract fixture for compatible upstreams. Live OpenAI cache response
	// verification is a separate release check; this is not a captured response.
	const usageJSON = `{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"input_tokens_details":{"text_tokens":400,"image_tokens":600,"cached_tokens":300,"cached_tokens_details":{"text_tokens":100,"image_tokens":200}}}`
	for _, mode := range []string{"image_generation", "image_edit"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, stream), func(t *testing.T) {
				body := `{"data":[{"b64_json":"image"}],"usage":` + usageJSON + `}`
				contentType := "application/json"
				if stream {
					body = "data: {\"type\":\"" + mode + ".completed\",\"usage\":" + usageJSON + "}\n\ndata: [DONE]\n\n"
					contentType = "text/event-stream"
				}
				ctx, recorder, response, info := newImageTestContext(t, body, contentType, stream)
				info.RelayMode = relayconstant.RelayModeImagesGenerations
				if mode == "image_edit" {
					info.RelayMode = relayconstant.RelayModeImagesEdits
					ctx.Request.URL.Path = "/v1/images/edits"
				}
				result, apiErr := (&Adaptor{}).DoResponse(ctx, response, info)
				require.Nil(t, apiErr)
				usage := result.(*dto.Usage)
				assert.Equal(t, 1000, usage.PromptTokens)
				assert.Equal(t, 300, usage.PromptTokensDetails.CachedTokens)
				require.NotNil(t, usage.PromptTokensDetails.CachedTokensDetails)
				assert.Equal(t, 200, *usage.PromptTokensDetails.CachedTokensDetails.ImageTokens)
				assert.Contains(t, recorder.Body.String(), `"cached_tokens_details":{"text_tokens":100,"image_tokens":200}`)
			})
		}
	}
}

func TestNormalizeOpenAIUsageMapsOutputImageTokens(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })

	const usageJSON = `{"input_tokens":15,"output_tokens":1352,"total_tokens":1367,"input_tokens_details":{"text_tokens":15,"image_tokens":0},"output_tokens_details":{"image_tokens":1120,"text_tokens":232}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			body := `{"data":[{"b64_json":"image"}],"usage":` + usageJSON + `}`
			contentType := "application/json"
			if stream {
				body = "data: {\"type\":\"image_generation.completed\",\"usage\":" + usageJSON + "}\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}
			ctx, _, response, info := newImageTestContext(t, body, contentType, stream)
			info.RelayMode = relayconstant.RelayModeImagesGenerations
			result, apiErr := (&Adaptor{}).DoResponse(ctx, response, info)
			require.Nil(t, apiErr)
			usage := result.(*dto.Usage)
			assert.Equal(t, 15, usage.PromptTokens)
			assert.Equal(t, 1352, usage.CompletionTokens)
			assert.Equal(t, 1120, usage.CompletionTokenDetails.ImageTokens)
			assert.Equal(t, 232, usage.CompletionTokenDetails.TextTokens)
		})
	}
}

func TestOpenaiImageDoResponseUsesInfoIsStream(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := `{"created":1710000000,"data":[{"b64_json":"image"}]}`

	t.Run("non-stream response stays JSON", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, body, "application/json", false)
		info.RelayMode = relayconstant.RelayModeImagesGenerations

		usage, err := (&Adaptor{}).DoResponse(c, resp, info)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Equal(t, body, recorder.Body.String())
	})

	t.Run("stream response converts JSON to SSE", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, body, "application/json", true)
		info.RelayMode = relayconstant.RelayModeImagesGenerations

		usage, err := (&Adaptor{}).DoResponse(c, resp, info)

		require.Nil(t, err)
		require.NotNil(t, usage)
		require.Contains(t, recorder.Body.String(), `event: image_generation.completed`)
		require.Contains(t, recorder.Body.String(), `data: [DONE]`)
	})
}

// TestOpenaiImageStreamHandlerForwardsSSEAndUsage covers the core SSE path:
// chunks are forwarded with rebuilt event lines, usage is extracted and
// normalized (input_tokens -> prompt_tokens with details), and [DONE] is
// re-emitted to the client.
func TestOpenaiImageStreamHandlerForwardsSSEAndUsage(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`event: image_generation.partial_image`,
		`data: {"type":"image_generation.partial_image","b64_json":"partial"}`,
		``,
		`data: {"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7,"input_tokens_details":{"image_tokens":2,"text_tokens":1}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	c, recorder, resp, info := newImageTestContext(t, body, "text/event-stream", true)
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 3)

	usage, err := OpenaiImageStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 7, usage.TotalTokens)
	require.Equal(t, 2, usage.PromptTokensDetails.ImageTokens)
	require.Equal(t, 1, usage.PromptTokensDetails.TextTokens)
	require.Contains(t, recorder.Body.String(), `event: image_generation.partial_image`)
	require.Contains(t, recorder.Body.String(), `data: {"type":"image_generation.partial_image","b64_json":"partial"}`)
	require.Contains(t, recorder.Body.String(), `data: {"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7,"input_tokens_details":{"image_tokens":2,"text_tokens":1}}}`)
	require.Contains(t, recorder.Body.String(), `data: [DONE]`)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, 3.0, info.PriceData.OtherRatios()["n"], "streams without completed events keep the requested count")
}

func TestOpenaiImageStreamHandlerUsesCompletedEventCount(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"image_generation.partial_image","partial_image_index":0,"b64_json":"partial"}`,
		``,
		`data: {"type":"image_generation.completed","b64_json":"first"}`,
		``,
		`data: {"type":"image_edit.completed","b64_json":"second","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	c, _, resp, info := newImageTestContext(t, body, "text/event-stream", true)
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 3)

	usage, err := OpenaiImageStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.Equal(t, 7, usage.TotalTokens)
	require.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
}

// blockingBody serves one SSE chunk, then blocks until Close (the scanner's
// cleanup) and returns EOF — keeping the upstream "open" while the client-side
// disconnect is simulated elsewhere.
type blockingBody struct {
	mu     sync.Mutex
	sent   bool
	chunk  []byte
	closed chan struct{}
}

func (b *blockingBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if !b.sent {
		b.sent = true
		n := copy(p, b.chunk)
		b.mu.Unlock()
		return n, nil
	}
	b.mu.Unlock()
	<-b.closed
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

// cancelAfterWriter cancels the request context right after the payload
// containing needle has been written to the client, simulating a client that
// disconnects after receiving that event. Cancelling from the write side (not
// the upstream read side) makes the abort deterministic: the handler has
// already processed and counted the event when the disconnect fires.
type cancelAfterWriter struct {
	gin.ResponseWriter
	needle string
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelAfterWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if strings.Contains(string(p), w.needle) {
		w.once.Do(w.cancel)
	}
	return n, err
}

func (w *cancelAfterWriter) WriteString(s string) (int, error) {
	n, err := io.WriteString(w.ResponseWriter, s)
	if strings.Contains(s, w.needle) {
		w.once.Do(w.cancel)
	}
	return n, err
}

func newDisconnectingImageStream(t *testing.T, sseBody, disconnectAfter string) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	c, recorder, resp, info := newImageTestContext(t, "", "text/event-stream", true)
	ctx, cancel := context.WithCancel(c.Request.Context())
	t.Cleanup(cancel)
	c.Request = c.Request.WithContext(ctx)
	c.Writer = &cancelAfterWriter{ResponseWriter: c.Writer, needle: disconnectAfter, cancel: cancel}
	resp.Body = &blockingBody{
		chunk:  []byte(sseBody),
		closed: make(chan struct{}),
	}
	return c, recorder, resp, info
}

// TestOpenaiImageStreamHandlerClientDisconnectKeepsRequestedCount guards the
// billing invariant: completed-event counting must not lower the charge when
// the client aborts the stream. Upstream already generated (and charged for)
// all requested images, so a disconnect after the first completed event keeps
// the requested n instead of dropping it to 1.
func TestOpenaiImageStreamHandlerClientDisconnectKeepsRequestedCount(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"first\"}\n\n"
	c, recorder, resp, info := newDisconnectingImageStream(t, body, "first")
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 3)

	usage, err := OpenaiImageStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, info.StreamStatus)
	// A client abort surfaces as client_gone (main-loop ctx watch) or
	// handler_stop (failed client write); both must be treated as untrusted.
	require.Contains(t,
		[]relaycommon.StreamEndReason{relaycommon.StreamEndReasonClientGone, relaycommon.StreamEndReasonHandlerStop},
		info.StreamStatus.EndReason)
	require.Contains(t, recorder.Body.String(), `"b64_json":"first"`)
	require.Equal(t, 3.0, info.PriceData.OtherRatios()["n"], "client abort must not reduce the billed image count")
}

// TestOpenaiImageStreamHandlerClientDisconnectRaisesCount covers the other
// direction of the abort guard: when completed events already exceed the
// recorded n, the higher actual count is billed even though the client aborted.
func TestOpenaiImageStreamHandlerClientDisconnectRaisesCount(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"type":"image_generation.completed","b64_json":"first"}`,
		``,
		`data: {"type":"image_generation.completed","b64_json":"second"}`,
		``,
		``,
	}, "\n")
	c, _, resp, info := newDisconnectingImageStream(t, body, "second")
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 1)

	usage, err := OpenaiImageStreamHandler(c, info, resp)

	require.Nil(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, info.StreamStatus)
	require.Contains(t,
		[]relaycommon.StreamEndReason{relaycommon.StreamEndReasonClientGone, relaycommon.StreamEndReasonHandlerStop},
		info.StreamStatus.EndReason)
	require.Equal(t, 2.0, info.PriceData.OtherRatios()["n"], "completed events beyond the recorded n must raise the charge even on abort")
}

// TestOpenaiImageStreamHandlerWrapsJSONResponse covers the non-SSE fallback:
// a JSON upstream response is wrapped into pseudo-SSE completed events.
func TestOpenaiImageStreamHandlerWrapsJSONResponse(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := `{"created":1710000000,"data":[{"b64_json":"first","revised_prompt":"draw a cat"},{"b64_json":"second"}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7,"input_tokens_details":{"image_tokens":2,"text_tokens":1}}}`

	c, recorder, resp, info := newImageTestContext(t, body, "application/json", true)
	info.PriceData.UsePrice = true
	info.PriceData.AddOtherRatio("n", 3)

	usage, err := OpenaiImageStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, 4, usage.CompletionTokens)
	require.Equal(t, 7, usage.TotalTokens)
	require.Equal(t, 2, usage.PromptTokensDetails.ImageTokens)
	require.Equal(t, 1, usage.PromptTokensDetails.TextTokens)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.Contains(t, recorder.Body.String(), `event: image_generation.completed`)
	require.Contains(t, recorder.Body.String(), `"type":"image_generation.completed"`)
	require.Contains(t, recorder.Body.String(), `"b64_json":"first"`)
	require.Contains(t, recorder.Body.String(), `"b64_json":"second"`)
	require.Contains(t, recorder.Body.String(), `"revised_prompt":"draw a cat"`)
	require.Contains(t, recorder.Body.String(), `data: [DONE]`)
	require.Equal(t, 2, strings.Count(recorder.Body.String(), `event: image_generation.completed`))
	require.Equal(t, 2.0, info.PriceData.OtherRatios()["n"])
}

func TestOpenaiImageHandlerUsesPositiveActualCountForFixedPrice(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	longImage := strings.Repeat("a", 4096)

	tests := []struct {
		name      string
		body      string
		usePrice  bool
		wantCount float64
	}{
		{
			name:      "fixed price uses data length",
			body:      `{"data":[{"b64_json":"` + longImage + `"},{"b64_json":"second"}]}`,
			usePrice:  true,
			wantCount: 2,
		},
		{
			name:      "empty data keeps requested count",
			body:      `{"data":[]}`,
			usePrice:  true,
			wantCount: 3,
		},
		{
			name:      "ratio billing ignores data length",
			body:      `{"data":[{"b64_json":"first"},{"b64_json":"second"}]}`,
			usePrice:  false,
			wantCount: 3,
		},
		{
			name:      "object data with url and b64_json counts one image",
			body:      `{"data":{"url":"https://example.com/a.png","b64_json":"` + longImage + `"}}`,
			usePrice:  true,
			wantCount: 1,
		},
		{
			name:      "url and b64_json split across entries count one image",
			body:      `{"data":[{"url":"https://example.com/a.png"},{"b64_json":"` + longImage + `"}]}`,
			usePrice:  true,
			wantCount: 1,
		},
		{
			name:      "entries without image payload keep requested count",
			body:      `{"data":[{"revised_prompt":"draw a cat"}]}`,
			usePrice:  true,
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, tt.body, "application/json", false)
			info.PriceData.UsePrice = tt.usePrice
			info.PriceData.AddOtherRatio("n", 3)

			_, err := OpenaiImageHandler(c, info, resp)

			require.Nil(t, err)
			require.Equal(t, tt.wantCount, info.PriceData.OtherRatios()["n"])
			require.Equal(t, tt.body, recorder.Body.String())
		})
	}
}

// TestOpenaiImageStreamHandlerWrapsNonStandardDataShapes covers the JSON-to-SSE
// fallback when upstream returns data as an object, splits one image across a
// url entry and a b64_json entry, or returns entries without any image payload.
// Forwarded events and the billed quantity must both follow the real images.
func TestOpenaiImageStreamHandlerWrapsNonStandardDataShapes(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	for _, tc := range []struct {
		name       string
		body       string
		wantEvents int
		wantCount  float64
		wantBody   []string
	}{
		{
			name:       "object data is forwarded as one completed event",
			body:       `{"data":{"url":"https://example.com/a.png","b64_json":"first"}}`,
			wantEvents: 1,
			wantCount:  1,
			wantBody:   []string{`"url":"https://example.com/a.png"`, `"b64_json":"first"`},
		},
		{
			name:       "split url and b64_json entries bill one image",
			body:       `{"data":[{"url":"https://example.com/a.png"},{"b64_json":"first"}]}`,
			wantEvents: 2,
			wantCount:  1,
			wantBody:   []string{`"url":"https://example.com/a.png"`, `"b64_json":"first"`},
		},
		{
			name:       "entries without image payload are dropped and keep requested count",
			body:       `{"data":[{"revised_prompt":"draw a cat"}]}`,
			wantEvents: 0,
			wantCount:  3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, tc.body, "application/json", true)
			info.PriceData.UsePrice = true
			info.PriceData.AddOtherRatio("n", 3)

			_, err := OpenaiImageStreamHandler(c, info, resp)
			require.Nil(t, err)
			out := recorder.Body.String()
			assert.Equal(t, tc.wantEvents, strings.Count(out, "event: image_generation.completed"))
			assert.Equal(t, tc.wantEvents, info.ReceivedResponseCount)
			for _, want := range tc.wantBody {
				assert.Contains(t, out, want)
			}
			assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))
			assert.Equal(t, tc.wantCount, info.PriceData.OtherRatios()["n"])
		})
	}
}

// TestOpenaiImageHandlersReturnJSONError covers JSON error responses for both
// entry points: the non-streaming handler and the stream handler's non-SSE
// fallback. Neither must leak the error body to the client.
func TestOpenaiImageHandlersReturnJSONError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	body := `{"error":{"message":"content moderation failed","type":"upstream_error","code":"content_moderation_failed","status":502}}`

	t.Run("non-streaming handler", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, body, "application/json", false)

		usage, err := OpenaiImageHandler(c, info, resp)
		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusOK, err.StatusCode)
		oaiError := err.ToOpenAIError()
		require.Equal(t, "content moderation failed", oaiError.Message)
		require.Equal(t, "upstream_error", oaiError.Type)
		require.Equal(t, "content_moderation_failed", oaiError.Code)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("stream handler JSON fallback", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, body, "application/json", true)

		usage, err := OpenaiImageStreamHandler(c, info, resp)
		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusOK, err.StatusCode)
		require.Equal(t, "content moderation failed", err.ToOpenAIError().Message)
		require.Empty(t, recorder.Body.String())
	})

	t.Run("stream handler non-2xx stays JSON error", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, body, "application/json", true)
		resp.StatusCode = http.StatusBadGateway

		usage, err := OpenaiImageStreamHandler(c, info, resp)
		require.Nil(t, usage)
		require.NotNil(t, err)
		require.Equal(t, http.StatusBadGateway, err.StatusCode)
		require.Equal(t, "content moderation failed", err.ToOpenAIError().Message)
		require.Empty(t, recorder.Body.String())
		require.NotContains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	})
}

// TestOpenaiImageStreamHandlerRecordsUpstreamErrorEvent verifies that an error
// event inside the SSE stream is recorded as a soft error while the payload is
// still forwarded to the client.
func TestOpenaiImageStreamHandlerRecordsUpstreamErrorEvent(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`event: image_generation.partial_image`,
		`data: {"type":"image_generation.partial_image","b64_json":"partial"}`,
		``,
		`event: error`,
		`data: {"type":"upstream_error","error":{"message":"stream error: stream ID 77; INTERNAL_ERROR; received from peer"}}`,
		``,
	}, "\n")

	c, recorder, resp, info := newImageTestContext(t, body, "text/event-stream", true)

	usage, err := OpenaiImageStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	require.NotNil(t, info.StreamStatus)
	require.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	require.True(t, info.StreamStatus.HasErrors())
	require.Equal(t, 1, info.StreamStatus.TotalErrorCount())
	require.Contains(t, info.StreamStatus.Errors[0].Message, "INTERNAL_ERROR")
	// The scanner strips the upstream "event: error" line; the event name is
	// rebuilt from the JSON "type" field (upstream_error). The error message
	// is still forwarded in the data: payload (stream ID 77).
	require.Contains(t, recorder.Body.String(), `event: upstream_error`)
	require.Contains(t, recorder.Body.String(), `stream ID 77`)
}

// novelAIZip builds the zip a NovelAI generate-image call returns, one entry
// per name.
func novelAIZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, name := range names {
		entry, err := archive.Create(name)
		require.NoError(t, err)
		if !strings.HasSuffix(name, "/") {
			_, err = entry.Write([]byte("\x89PNG\r\n\x1a\n" + name))
			require.NoError(t, err)
		}
	}
	require.NoError(t, archive.Close())
	return buf.Bytes()
}

// failingWriter fails every client write after the first, like a client
// that disconnected mid-body.
type failingWriter struct {
	gin.ResponseWriter
	writes int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("client disconnected")
	}
	return w.ResponseWriter.Write(p)
}

// A NovelAI response settles at the images it carries: the .png entries of a
// zip, or the images[] entries with a non-empty image in JSON, never the array
// length. A body that cannot be counted, a client that leaves or an upstream
// that fails mid-body keeps the reserved quantity, under a per-image price and
// under an image_count expression alike. The client receives the upstream
// status, headers and bytes unchanged. A client that leaves is no perf-metrics
// health sample, as on the relay streams, while a completed body is a success.
func TestNovelAIImageHandlerSettlesActualImageCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requested = 4
	images := novelAIZip(t, "image_0.png", "image_1.PNG", "metadata.json", "thumbs/")

	tests := []struct {
		name, contentType string
		body              []byte
		// upstream and client failures; the body is still forwarded up to them
		upstreamFailsAfterBody bool
		// how the client leaves mid-body: a write to it fails, or its request
		// context ends the upstream read
		clientLeaves string
		want         int
	}{
		{name: "zip counts its png entries", contentType: "application/zip", body: images, want: 2},
		{name: "zip is recognized without its media type", contentType: "application/octet-stream", body: images, want: 2},
		{name: "JSON counts images with a payload", contentType: "application/json", body: []byte(`{"images":[{"image":"aGk="},{"image":""},{"image":"aGk="},{"seed":7}]}`), want: 2},
		{name: "JSON object images counts one", contentType: "application/json", body: []byte(`{"images":{"image":"aGk="}}`), want: 1},
		{name: "JSON without image payloads keeps the reservation", contentType: "application/json", body: []byte(`{"images":[{"image":""},{"seed":7}]}`), want: requested},
		{name: "msgpack stream events keep the reservation", contentType: "application/x-msgpack", body: []byte("\x82\xaaevent_type\xa5final\xa5image\xc4\x01x"), want: requested},
		{name: "corrupt zip keeps the reservation", contentType: "application/zip", body: images[:24], want: requested},
		{name: "upstream failing after the body keeps the reservation", contentType: "application/zip", body: images, upstreamFailsAfterBody: true, want: requested},
		{name: "client leaving mid-body keeps the reservation", contentType: "application/zip", body: images, clientLeaves: "write fails", want: requested},
		{name: "client leaving between upstream reads keeps the reservation", contentType: "application/zip", body: images, clientLeaves: "read ends", want: requested},
	}
	for _, tc := range tests {
		for _, pricing := range []string{"per-image price", "image_count expression"} {
			t.Run(tc.name+"/"+pricing, func(t *testing.T) {
				c, recorder, resp, info := newImageTestContext(t, "", tc.contentType, false)
				var body io.Reader = bytes.NewReader(tc.body)
				if tc.upstreamFailsAfterBody {
					body = io.MultiReader(body, iotest.ErrReader(errors.New("connection reset by peer")))
				}
				switch tc.clientLeaves {
				case "write fails":
					body = io.MultiReader(bytes.NewReader(tc.body[:16]), bytes.NewReader(tc.body[16:]))
					c.Writer = &failingWriter{ResponseWriter: c.Writer}
				case "read ends":
					ctx, cancel := context.WithCancel(c.Request.Context())
					t.Cleanup(cancel)
					c.Request = c.Request.WithContext(ctx)
					body = io.MultiReader(bytes.NewReader(tc.body[:16]), clientLeavingReader{cancel: cancel})
				}
				resp.Body = io.NopCloser(body)
				resp.ContentLength = int64(len(tc.body))
				resp.Header.Set("Content-Disposition", `attachment; filename="images.zip"`)
				resp.Header.Set("X-Upstream-Internal", "not forwarded")
				if pricing == "per-image price" {
					info.PriceData.UsePrice = true
					info.PriceData.AddOtherRatio("n", requested)
				} else {
					info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{EstimatedImageCount: common.GetPointer(requested)}
				}

				usage, apiErr := NovelAIImageHandler(c, info, resp)

				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Equal(t, tc.contentType, recorder.Header().Get("Content-Type"))
				assert.Equal(t, `attachment; filename="images.zip"`, recorder.Header().Get("Content-Disposition"))
				assert.Equal(t, strconv.Itoa(len(tc.body)), recorder.Header().Get("Content-Length"))
				assert.Empty(t, recorder.Header().Get("X-Upstream-Internal"))
				outcome := perfmetrics.ClassifyRelayOutcome(c.Request.Context(), info, apiErr)
				if tc.clientLeaves != "" {
					assert.Equal(t, tc.body[:16], recorder.Body.Bytes())
					assert.Equal(t, perfmetrics.OutcomeIgnored, outcome)
				} else {
					assert.Equal(t, tc.body, recorder.Body.Bytes())
				}
				if tc.clientLeaves == "" && !tc.upstreamFailsAfterBody {
					assert.Equal(t, perfmetrics.OutcomeSuccess, outcome)
				}
				if pricing == "per-image price" {
					assert.Equal(t, float64(tc.want), info.PriceData.OtherRatios()["n"])
					return
				}
				settled := info.RequestedImageCount()
				if info.BillingImageCount != nil {
					settled = *info.BillingImageCount
				}
				assert.Equal(t, tc.want, settled)
				assert.Empty(t, info.PriceData.OtherRatios(), "expression quantities must not add a legacy multiplier")
			})
		}
	}

	t.Run("upstream failing before the first byte fails the attempt", func(t *testing.T) {
		c, recorder, resp, info := newImageTestContext(t, "", "application/zip", false)
		resp.Body = io.NopCloser(iotest.ErrReader(errors.New("connection reset by peer")))
		info.PriceData.UsePrice = true
		info.PriceData.AddOtherRatio("n", requested)

		usage, apiErr := NovelAIImageHandler(c, info, resp)

		assert.Nil(t, usage)
		require.NotNil(t, apiErr)
		assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
		assert.False(t, c.Writer.Written(), "a retry or the error response still needs the untouched writer")
		assert.Empty(t, recorder.Body.Bytes())
	})

	// Upstream answered 2xx and nothing reached the client yet. A client that
	// leaves once upstream has started the body pays the reserved count: the
	// images exist. Before the first body byte (a stall the client or its
	// proxy gave up on, or a stream that has not generated anything), and on
	// an upstream failure with the client still there, the attempt fails. A
	// client that leaves is no perf-metrics sample either way.
	for _, tc := range []struct {
		name, accept string
		mode         int
		prefix       []byte
		clientLeaves bool
		billed       bool
	}{
		{name: "stream endpoint: client leaving before the first body byte fails the attempt", mode: relayconstant.RelayModeNovelAIGenerateImageStream, clientLeaves: true},
		{name: "generate-image: client leaving before the first body byte fails the attempt", mode: relayconstant.RelayModeNovelAIGenerateImage, clientLeaves: true},
		{name: "generate-image: client leaving after the first body bytes keeps the reservation", mode: relayconstant.RelayModeNovelAIGenerateImage, prefix: images[:2], clientLeaves: true, billed: true},
		{name: "generate-image: client leaving while a zip is held for conversion to JSON keeps the reservation", accept: "application/json", mode: relayconstant.RelayModeNovelAIGenerateImage, prefix: images[:40], clientLeaves: true, billed: true},
		{name: "generate-image: upstream failing while a zip is held for conversion to JSON fails the attempt", accept: "application/json", mode: relayconstant.RelayModeNovelAIGenerateImage, prefix: images[:40]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, "", "application/zip", false)
			info.RelayMode = tc.mode
			if tc.accept != "" {
				c.Request.Header.Set("Accept", tc.accept)
			}
			ctx, cancel := context.WithCancel(c.Request.Context())
			t.Cleanup(cancel)
			c.Request = c.Request.WithContext(ctx)
			var failure io.Reader = iotest.ErrReader(errors.New("connection reset by peer"))
			if tc.clientLeaves {
				// The upstream read ends with the client's request context.
				failure = clientLeavingReader{cancel: cancel}
			}
			resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(tc.prefix), failure))
			info.PriceData.UsePrice = true
			info.PriceData.AddOtherRatio("n", requested)

			usage, apiErr := NovelAIImageHandler(c, info, resp)

			assert.Empty(t, recorder.Body.Bytes())
			if tc.clientLeaves {
				assert.Equal(t, perfmetrics.OutcomeIgnored, perfmetrics.ClassifyRelayOutcome(ctx, info, apiErr))
			}
			if !tc.billed {
				assert.Nil(t, usage)
				require.NotNil(t, apiErr)
				assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
				assert.Equal(t, types.ErrorTypeOpenAIError, apiErr.GetErrorType(), "an upstream read failure is not a new-api rejection")
				assert.False(t, c.Writer.Written(), "the error response still needs the untouched writer")
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, float64(requested), info.PriceData.OtherRatios()["n"])
		})
	}
}

// clientLeavingReader is an upstream body whose read ends because the client
// left: it cancels the client's request context and fails with it.
type clientLeavingReader struct {
	cancel context.CancelFunc
}

func (r clientLeavingReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, context.Canceled
}

// flushNotifyWriter reports what the client has received at every flush.
type flushNotifyWriter struct {
	gin.ResponseWriter
	recorder *httptest.ResponseRecorder
	flushed  chan<- string
}

func (w *flushNotifyWriter) Flush() {
	w.ResponseWriter.Flush()
	w.flushed <- w.recorder.Body.String()
}

// The stream endpoint hands each upstream read to the client as it arrives
// instead of buffering the response until upstream finishes, and so does the
// generate-image endpoint once the first bytes show the response is already
// in the format the client asked for.
func TestNovelAIImageHandlerFlushesBeforeUpstreamFinishes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, accept, contentType string
		relayMode                 int
		first, second             string
	}{
		{name: "stream events", contentType: "application/x-msgpack", relayMode: relayconstant.RelayModeNovelAIGenerateImageStream, first: "first event", second: "second event"},
		{name: "zip without Accept", contentType: "application/zip", relayMode: relayconstant.RelayModeNovelAIGenerateImage, first: "PK\x03\x04first entry", second: "second entry"},
		{name: "JSON asked as JSON", accept: "application/json", contentType: "application/json", relayMode: relayconstant.RelayModeNovelAIGenerateImage, first: `{"images":[`, second: `{"image":"aGk="}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, "", tc.contentType, tc.relayMode == relayconstant.RelayModeNovelAIGenerateImageStream)
			info.RelayMode = tc.relayMode
			if tc.accept != "" {
				c.Request.Header.Set("Accept", tc.accept)
			}
			upstream, upstreamWriter := io.Pipe()
			t.Cleanup(func() { _ = upstreamWriter.CloseWithError(io.ErrClosedPipe) })
			resp.Body = upstream
			resp.ContentLength = -1
			flushed := make(chan string, 4)
			c.Writer = &flushNotifyWriter{ResponseWriter: c.Writer, recorder: recorder, flushed: flushed}

			done := make(chan struct{})
			go func() {
				defer close(done)
				_, apiErr := NovelAIImageHandler(c, info, resp)
				assert.Nil(t, apiErr)
			}()

			_, err := upstreamWriter.Write([]byte(tc.first))
			require.NoError(t, err)
			select {
			case received := <-flushed:
				assert.Equal(t, tc.first, received)
			case <-time.After(5 * time.Second):
				_ = upstreamWriter.CloseWithError(io.ErrClosedPipe)
				<-done
				require.FailNow(t, "the first bytes did not reach the client while upstream was still open")
			}
			_, err = upstreamWriter.Write([]byte(tc.second))
			require.NoError(t, err)
			require.NoError(t, upstreamWriter.Close())
			<-done

			assert.Equal(t, tc.first+tc.second, recorder.Body.String())
			assert.Equal(t, tc.contentType, recorder.Header().Get("Content-Type"))
			assert.Empty(t, recorder.Header().Get("Content-Length"))
		})
	}
}

// deadlineWriter counts the client writes made without a write deadline that
// http.ResponseController set beforehand.
type deadlineWriter struct {
	gin.ResponseWriter
	deadline                time.Time
	writes, withoutDeadline int
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	w.writes++
	if !w.deadline.After(time.Now()) {
		w.withoutDeadline++
	}
	w.deadline = time.Time{}
	return w.ResponseWriter.Write(p)
}

// An upstream that sends nothing for STREAMING_TIMEOUT no longer holds the
// client, as on the other relay streams. Once the client has data, the relay
// ends there and keeps the reserved image count; with nothing sent yet, the
// attempt fails with the writer untouched, for a retry or the error response.
// Each client write runs under a write deadline, so a client that stops
// reading cannot hold the relay either.
func TestNovelAIImageHandlerEndsStalledResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = previous })
	const requested = 4

	for _, sent := range []string{"first event", ""} {
		t.Run(fmt.Sprintf("stalls after %q", sent), func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, "", "application/x-msgpack", true)
			upstream, upstreamWriter := io.Pipe()
			t.Cleanup(func() { _ = upstreamWriter.CloseWithError(io.ErrClosedPipe) })
			resp.Body = upstream
			resp.ContentLength = -1
			info.PriceData.UsePrice = true
			info.PriceData.AddOtherRatio("n", requested)
			writer := &deadlineWriter{ResponseWriter: c.Writer}
			c.Writer = writer

			done := make(chan struct{})
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			go func() {
				defer close(done)
				usage, apiErr = NovelAIImageHandler(c, info, resp)
			}()
			if sent != "" {
				_, err := upstreamWriter.Write([]byte(sent))
				require.NoError(t, err)
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				require.FailNow(t, "the relay kept waiting on an upstream that sent nothing")
			}

			assert.Equal(t, float64(requested), info.PriceData.OtherRatios()["n"])
			if sent == "" {
				assert.Nil(t, usage)
				require.NotNil(t, apiErr)
				assert.False(t, c.Writer.Written())
				return
			}
			require.Nil(t, apiErr)
			assert.NotNil(t, usage)
			assert.Equal(t, sent, recorder.Body.String())
			assert.Equal(t, 1, writer.writes)
			assert.Zero(t, writer.withoutDeadline)
		})
	}
}

// novelAIPNGZip archives pngs as NovelAI names them, image_0.png,
// image_1.png, ..., stored and deflated in turn.
func novelAIPNGZip(t *testing.T, pngs ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for i, png := range pngs {
		method := zip.Store
		if i%2 == 1 {
			method = zip.Deflate
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("image_%d.png", i), Method: method})
		require.NoError(t, err)
		_, err = entry.Write(png)
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	return buf.Bytes()
}

// novelAIClientImages reads the images a NovelAI client gets from a response
// body: the entries of a zip in archive order, with their names and methods,
// or the non-empty images[] of JSON, base64 decoded.
func novelAIClientImages(t *testing.T, contentType string, body []byte) (images [][]byte, names []string, methods []uint16) {
	t.Helper()
	if contentType == "application/json" {
		var payload struct {
			Images []struct {
				Image string `json:"image"`
			} `json:"images"`
		}
		require.NoError(t, common.Unmarshal(body, &payload))
		for _, image := range payload.Images {
			if image.Image == "" {
				continue
			}
			png, err := base64.StdEncoding.DecodeString(image.Image)
			require.NoError(t, err)
			images = append(images, png)
		}
		return images, nil, nil
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	for _, file := range archive.File {
		entry, err := file.Open()
		require.NoError(t, err)
		png, err := io.ReadAll(entry)
		require.NoError(t, err)
		require.NoError(t, entry.Close())
		images, names, methods = append(images, png), append(names, file.Name), append(methods, file.Method)
	}
	return images, names, methods
}

// /ai/generate-image answers as NovelAI does: a zip unless Accept names
// application/json (in any case), then JSON, whichever format upstream chose.
// A response already in that format goes through as sent; one in the other
// format is converted, keeps the upstream status, carries its own
// Content-Type and Content-Length and no upstream Content-Disposition. A
// converted zip is typed application/x-zip-compressed, which novelai-api
// reads as a zip where it rejects application/zip, and each stored entry
// carries its CRC and sizes in its local header, as streaming readers such as
// Java's ZipInputStream need. Either way the call settles at the images the
// client received.
func TestNovelAIImageHandlerAnswersInTheAcceptedFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pngs := [][]byte{[]byte("\x89PNG\r\n\x1a\nfirst image"), []byte("\x89PNG\r\n\x1a\nsecond image")}
	encoded := func(png []byte) string { return base64.StdEncoding.EncodeToString(png) }
	upstreams := []struct {
		format, contentType, disposition string
		body                             []byte
		// the images the body carries, in order
		pngs [][]byte
	}{
		{format: "zip", contentType: "application/zip", disposition: `attachment; filename="images.zip"`, body: novelAIPNGZip(t, pngs...), pngs: pngs},
		{format: "JSON", contentType: "application/json", body: []byte(`{"images":[{"image":"` + encoded(pngs[0]) + `","seed":1},{"image":""},{"image":"` + encoded(pngs[1]) + `"}]}`), pngs: pngs},
		{format: "JSON object", contentType: "application/json", body: []byte(`{"images":{"image":"` + encoded(pngs[0]) + `","seed":1}}`), pngs: pngs[:1]},
	}
	for _, client := range []struct {
		accept   string
		wantJSON bool
	}{
		{accept: ""},
		{accept: "*/*"},
		{accept: "application/json", wantJSON: true},
		{accept: "application/json, text/plain", wantJSON: true},
		{accept: "APPLICATION/JSON", wantJSON: true},
	} {
		for _, upstream := range upstreams {
			t.Run(fmt.Sprintf("Accept %q/%s upstream", client.accept, upstream.format), func(t *testing.T) {
				c, recorder, resp, info := newImageTestContext(t, "", upstream.contentType, false)
				info.RelayMode = relayconstant.RelayModeNovelAIGenerateImage
				if client.accept != "" {
					c.Request.Header.Set("Accept", client.accept)
				}
				resp.StatusCode = http.StatusCreated
				resp.Body = io.NopCloser(bytes.NewReader(upstream.body))
				resp.ContentLength = int64(len(upstream.body))
				if upstream.disposition != "" {
					resp.Header.Set("Content-Disposition", upstream.disposition)
				}
				info.PriceData.UsePrice = true
				info.PriceData.AddOtherRatio("n", 4)

				usage, apiErr := NovelAIImageHandler(c, info, resp)

				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				assert.Equal(t, http.StatusCreated, recorder.Code)
				assert.Equal(t, strconv.Itoa(recorder.Body.Len()), recorder.Header().Get("Content-Length"))
				assert.Equal(t, float64(len(upstream.pngs)), info.PriceData.OtherRatios()["n"])
				if passthrough := client.wantJSON == (upstream.format != "zip"); passthrough {
					assert.Equal(t, upstream.contentType, recorder.Header().Get("Content-Type"))
					assert.Equal(t, upstream.body, recorder.Body.Bytes())
					assert.Equal(t, upstream.disposition, recorder.Header().Get("Content-Disposition"))
					return
				}
				assert.Empty(t, recorder.Header().Get("Content-Disposition"))
				if client.wantJSON {
					assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
					images, _, _ := novelAIClientImages(t, "application/json", recorder.Body.Bytes())
					assert.Equal(t, upstream.pngs, images)
					return
				}
				assert.Equal(t, "application/x-zip-compressed", recorder.Header().Get("Content-Type"))
				images, names, methods := novelAIClientImages(t, "application/x-zip-compressed", recorder.Body.Bytes())
				assert.Equal(t, upstream.pngs, images)
				assert.Equal(t, []string{"image_0.png", "image_1.png"}[:len(upstream.pngs)], names)
				assert.Equal(t, []uint16{zip.Store, zip.Store}[:len(upstream.pngs)], methods)
				// Read entry by entry from the local headers, as a streaming
				// reader does: no data descriptor follows the stored bytes.
				local := recorder.Body.Bytes()
				for _, png := range upstream.pngs {
					require.True(t, len(local) >= 30 && bytes.HasPrefix(local, []byte("PK\x03\x04")), "a local file header")
					assert.Zero(t, binary.LittleEndian.Uint16(local[6:])&0x8, "the data descriptor flag")
					assert.Equal(t, crc32.ChecksumIEEE(png), binary.LittleEndian.Uint32(local[14:]), "the CRC")
					assert.Equal(t, []uint32{uint32(len(png)), uint32(len(png))}, []uint32{binary.LittleEndian.Uint32(local[18:]), binary.LittleEndian.Uint32(local[22:])}, "the sizes")
					start := 30 + int(binary.LittleEndian.Uint16(local[26:])) + int(binary.LittleEndian.Uint16(local[28:]))
					require.GreaterOrEqual(t, len(local), start+len(png))
					assert.Equal(t, png, local[start:start+len(png)])
					local = local[start+len(png):]
				}
				assert.True(t, bytes.HasPrefix(local, []byte("PK\x01\x02")), "the central directory follows the last entry")
			})
		}
	}
}

// A response that cannot be converted to the wanted format reaches the client
// as upstream sent it and settles at the images it carries, keeping the
// reservation when they cannot be counted: JSON without an image or with an
// invalid one, a corrupt zip or one without PNG entries, JSON or a zip with
// more images than a request may ask for (a count billing does not take), a
// zip whose entries expand past the response limit whatever its headers
// claim, a body past the limit (what was held goes out, the rest streams
// after it), an encoded body, a body of neither format, and anything on the
// stream endpoint.
func TestNovelAIImageHandlerSendsUnconvertibleResponsesAsReceived(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := novelAIResponseLimit
	novelAIResponseLimit = 1 << 20
	t.Cleanup(func() { novelAIResponseLimit = previous })
	const requested = 4

	var bomb bytes.Buffer
	bombWriter := zip.NewWriter(&bomb)
	entry, err := bombWriter.Create("image_0.png")
	require.NoError(t, err)
	_, err = entry.Write(make([]byte, 2*novelAIResponseLimit))
	require.NoError(t, err)
	require.NoError(t, bombWriter.Close())
	require.Less(t, bomb.Len(), novelAIResponseLimit, "the archive itself fits the limit")
	oversized := []byte(`{"images":[{"image":"` + strings.Repeat("aGk=", novelAIResponseLimit/4) + `"}]}`)
	tooManyImages := []byte(`{"images":[` + strings.Repeat(`{"image":"aGk="},`, dto.MaxImageN) + `{"image":"aGk="}]}`)
	tooManyPNGs := novelAIPNGZip(t, slices.Repeat([][]byte{[]byte("\x89PNG")}, dto.MaxImageN+1)...)

	for _, tc := range []struct {
		name, accept, contentType, encoding string
		body                                []byte
		stream                              bool
		want                                int
	}{
		{name: "JSON without images asked as zip", contentType: "application/json", body: []byte(`{"images":[{"image":""},{"seed":7}]}`), want: requested},
		{name: "JSON with invalid base64 asked as zip", contentType: "application/json", body: []byte(`{"images":[{"image":"aGk="},{"image":"not base64!"}]}`), want: 2},
		{name: "corrupt zip asked as JSON", accept: "application/json", contentType: "application/zip", body: novelAIPNGZip(t, []byte("\x89PNG"))[:30], want: requested},
		{name: "zip without PNG entries asked as JSON", accept: "application/json", contentType: "application/zip", body: novelAIZip(t, "metadata.json", "thumbs/"), want: requested},
		{name: "zip bomb asked as JSON", accept: "application/json", contentType: "application/zip", body: bomb.Bytes(), want: requested},
		{name: "JSON with more images than a request may ask for asked as zip", contentType: "application/json", body: tooManyImages, want: requested},
		{name: "zip with more PNG entries than a request may ask for asked as JSON", accept: "application/json", contentType: "application/zip", body: tooManyPNGs, want: requested},
		{name: "JSON past the limit asked as zip", contentType: "application/json", body: oversized, want: requested},
		// Bytes under an encoding the relay does not decode are left alone,
		// even when they happen to read as JSON.
		{name: "encoded JSON asked as zip", contentType: "application/json", encoding: "br", body: []byte(`{"images":[{"image":"aGk="}]}`), want: 1},
		{name: "msgpack asked as zip", contentType: "application/x-msgpack", body: []byte("\x82\xaaevent_type\xa5final"), want: requested},
		{name: "stream endpoint JSON without Accept", contentType: "application/json", body: []byte(`{"images":[{"image":"aGk="},{"image":"aGk="}]}`), stream: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder, resp, info := newImageTestContext(t, "", tc.contentType, tc.stream)
			info.RelayMode = relayconstant.RelayModeNovelAIGenerateImage
			if tc.stream {
				info.RelayMode = relayconstant.RelayModeNovelAIGenerateImageStream
			}
			if tc.accept != "" {
				c.Request.Header.Set("Accept", tc.accept)
			}
			if tc.encoding != "" {
				resp.Header.Set("Content-Encoding", tc.encoding)
			}
			resp.Body = io.NopCloser(bytes.NewReader(tc.body))
			resp.ContentLength = int64(len(tc.body))
			info.PriceData.UsePrice = true
			info.PriceData.AddOtherRatio("n", requested)

			usage, apiErr := NovelAIImageHandler(c, info, resp)

			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, tc.contentType, recorder.Header().Get("Content-Type"))
			assert.Equal(t, tc.encoding, recorder.Header().Get("Content-Encoding"))
			assert.Equal(t, strconv.Itoa(len(tc.body)), recorder.Header().Get("Content-Length"))
			assert.True(t, bytes.Equal(tc.body, recorder.Body.Bytes()), "the client must receive the upstream bytes")
			assert.Equal(t, float64(tc.want), info.PriceData.OtherRatios()["n"])
		})
	}
}
