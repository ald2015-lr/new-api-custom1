package relay

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/cloudflare"
	"github.com/QuantumNous/new-api/relay/channel/coze"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/zhipu"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leavingClient is the client's connection; like net/http it reports itself
// closed once the client is gone. answered is closed once the answer has
// reached it.
type leavingClient struct {
	*httptest.ResponseRecorder
	gone         context.Context
	answered     chan struct{}
	answeredOnce sync.Once
}

func (w *leavingClient) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), "forty-two.") {
		w.answeredOnce.Do(func() { close(w.answered) })
	}
}

func (w *leavingClient) CloseNotify() <-chan bool {
	closed := make(chan bool, 1)
	context.AfterFunc(w.gone, func() { closed <- true })
	return closed
}

// clientBoundBody is an upstream body read through a request bound to the
// client: once the received bytes are used up and the client has the answer
// (when one is expected), the client leaves and the next read fails with its
// cancellation, as the HTTP transport reports it.
type clientBoundBody struct {
	received *strings.Reader
	answered <-chan struct{}
	leave    context.CancelFunc
}

func (b *clientBoundBody) Read(p []byte) (int, error) {
	if b.received.Len() > 0 {
		return b.received.Read(p)
	}
	if b.answered != nil {
		// Handlers that relay through a goroutine write after this read;
		// the client leaves only once it has read the answer.
		select {
		case <-b.answered:
		case <-time.After(5 * time.Second):
		}
	}
	b.leave()
	return 0, context.Canceled
}

func (b *clientBoundBody) Close() error { return nil }

// The upstream request follows the client, so a client that leaves cancels the
// upstream read, often in the middle of a line. A stream is then billed for
// what the client received, a non-stream request that delivered nothing fails
// so it is refunded, and neither is logged as an error or a warning.
func TestChannelHandlersWhenClientLeaves(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	var errorLogs bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &errorLogs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})

	for _, tc := range []struct {
		name     string
		adaptor  channel.Adaptor
		mode     int
		format   types.RelayFormat
		stream   bool
		received string
		// refunded: nothing reached the client, so the handler fails.
		refunded bool
	}{
		{
			name: "coze stream", adaptor: &coze.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, stream: true,
			received: "event: conversation.message.delta\n" + `data: {"role":"assistant","type":"answer","content":"\"The answer is \""}` + "\n\n" +
				"event: conversation.message.delta\n" + `data: {"role":"assistant","type":"answer","content":"\"forty-two.\""}` + "\n\n",
		},
		{
			name: "cloudflare stream cut mid-line", adaptor: &cloudflare.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, stream: true,
			received: `data: {"choices":[{"index":0,"delta":{"content":"The answer is "}}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{"content":"forty-two."}}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"del`,
		},
		{
			// The last line is read whole but can no longer be written.
			name: "cloudflare stream cut after a line", adaptor: &cloudflare.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, stream: true,
			received: `data: {"choices":[{"index":0,"delta":{"content":"The answer is "}}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{"content":"forty-two."}}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{"content":" Really."}}]}`,
		},
		{
			// A truncated last line used to fail the request, which refunded
			// everything the client had received.
			name: "claude stream cut mid-line", adaptor: &claude.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, format: types.RelayFormatClaude, stream: true,
			received: "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
				"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The answer is "}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"forty-two."}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_del`,
		},
		{
			name: "gemini stream cut mid-line", adaptor: &gemini.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, format: types.RelayFormatOpenAI, stream: true,
			received: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"The answer is "}]},"index":0}]}` + "\n\n" +
				`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"forty-two."}]},"index":0}]}` + "\n\n" +
				`data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo`,
		},
		{
			name: "zhipu stream", adaptor: &zhipu.Adaptor{}, mode: relayconstant.RelayModeChatCompletions, stream: true,
			received: "data:The answer is \ndata:forty-two.\n",
		},
		{
			name: "speech", adaptor: &openai.Adaptor{}, mode: relayconstant.RelayModeAudioSpeech,
			received: "ID3\x04\x00\x00\x00\x00\x00\x00", refunded: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errorLogs.Reset()
			clientContext, leave := context.WithCancel(context.Background())
			t.Cleanup(leave)
			client := &leavingClient{ResponseRecorder: httptest.NewRecorder(), gone: clientContext, answered: make(chan struct{})}
			c, _ := gin.CreateTestContext(client)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(clientContext)
			info := &relaycommon.RelayInfo{
				RelayMode: tc.mode, RelayFormat: tc.format, IsStream: tc.stream, ShouldIncludeUsage: true, DisablePing: true,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-4o"},
			}
			info.SetEstimatePromptTokens(10)

			body := &clientBoundBody{received: strings.NewReader(tc.received), leave: leave}
			if !tc.refunded {
				body.answered = client.answered
			}
			usage, apiErr := tc.adaptor.DoResponse(c, &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
			}, info)
			assert.NotContains(t, errorLogs.String(), "[ERR]", "a client leaving is not an upstream failure")
			assert.NotContains(t, errorLogs.String(), "[WARN]", "a client leaving is not an upstream failure")
			if tc.refunded {
				require.NotNil(t, apiErr)
				return
			}
			require.Nil(t, apiErr)
			billed, ok := usage.(*dto.Usage)
			require.True(t, ok)
			require.NotNil(t, billed)
			assert.Equal(t, 10, billed.PromptTokens)
			assert.Positive(t, billed.CompletionTokens, "the delivered text is billed")
			assert.Equal(t, billed.PromptTokens+billed.CompletionTokens, billed.TotalTokens)
			assert.Contains(t, client.Body.String(), "forty-two.")
		})
	}
}
