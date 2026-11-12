package novelai

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// generateImagePath is NovelAI's non-stream generation endpoint, which
// answers with a zip of the images.
const generateImagePath = "/ai/generate-image"

// Adaptor relays NovelAI's official image API.
//
// The NovelAI native routes (relay.NovelAIImageHelper) relay as an
// OpenAI-type channel in front of a NovelAI-compatible forwarder does: the
// client body goes byte for byte to the base URL + /ai/generate-image or
// /ai/generate-image-stream with the channel key as a Bearer token, and
// openai.NovelAIImageHandler returns the response and counts the images.
//
// OpenAI images requests (/v1/images/generations and /v1/images/edits) and
// chat completions are converted: BuildGeneration makes the NovelAI request,
// /ai/generate-image answers it (/ai/generate-image-stream for an images
// stream on V4 and later), and the images come back in the OpenAI response
// shape, billed per image received.
//
// Every other route fails with a 400. On NovelAI's own host the generations
// of one key run one at a time (holdGeneration).
type Adaptor struct {
	// generation describes the converted request, set by
	// ConvertImageRequest or ConvertOpenAIRequest; output is how an images
	// client wants its images, and streamed whether NovelAI streams them.
	generation *Generation
	output     ImageOutput
	streamed   bool
}

// ErrNativeOnly rejects a request this channel cannot serve. It is final:
// another channel would not serve a NovelAI model on that route either.
func ErrNativeOnly(c *gin.Context) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgNovelAIChannelNativeOnly)), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// isImagesMode tells the OpenAI images routes the adaptor converts.
func isImagesMode(relayMode int) bool {
	return relayMode == relayconstant.RelayModeImagesGenerations || relayMode == relayconstant.RelayModeImagesEdits
}

// isConvertedMode tells every OpenAI route the adaptor converts: the images
// routes and chat completions.
func isConvertedMode(relayMode int) bool {
	return isImagesMode(relayMode) || relayMode == relayconstant.RelayModeChatCompletions
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	path, ok := relayconstant.NovelAIPath(info.RelayMode)
	if !ok {
		if !isConvertedMode(info.RelayMode) || a.generation == nil {
			return "", ErrNativeOnly(nil)
		}
		path = generateImagePath
		if a.streamed {
			path = generateImageStreamPath
		}
	}
	baseURL := strings.TrimSuffix(info.ChannelBaseUrl, "/")
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(constant.ChannelTypeNovelAI)
	}
	return baseURL + path, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if isConvertedMode(info.RelayMode) {
		// The converted request is JSON, and NovelAI answers it with its
		// default zip, or its event stream; a client's Accept (the OpenAI SDK
		// sends application/json) would ask for its JSON mode instead.
		header.Set("Content-Type", "application/json")
		header.Del("Accept")
		if a.streamed {
			header.Set("Accept", "text/event-stream")
		}
	} else {
		openai.SetNovelAIContentHeaders(c, header)
	}
	// A header override for Authorization is applied after this.
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

// DoRequest sends the request once the key's generation slot is free on
// NovelAI's own host. A send that failed after it may have reached NovelAI
// is final: NovelAI may have spent Anlas on it.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	// Checked here as well as by the request conversions, which a pass-through
	// body skips.
	if _, ok := relayconstant.NovelAIPath(info.RelayMode); !ok {
		if !isConvertedMode(info.RelayMode) {
			return nil, ErrNativeOnly(c)
		}
		if a.generation == nil {
			return nil, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgNovelAIPassThroughUnsupported)), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	release, apiErr := holdGeneration(c, info)
	if apiErr != nil {
		return nil, apiErr
	}
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	if err != nil {
		release()
		if RequestMayHaveReachedUpstream(err) {
			return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
		}
		return nil, err
	}
	if isNovelAIImageHost(info.ChannelBaseUrl) {
		// The key's slot is free once NovelAI's answer arrived, whatever
		// the client still has to receive.
		resp.Body = newGenerationBody(resp.Body, release)
	}
	if !isConvertedMode(info.RelayMode) {
		return resp, nil
	}
	if resp.StatusCode == http.StatusCreated {
		// NovelAI's documented success status; the image and chat relays
		// take 200.
		resp.StatusCode = http.StatusOK
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// The image and chat relays read an error answer whole, and leave it
		// open when the read fails: it is read here, bounded as on the
		// native routes, and closed, which frees the slot for a retry.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		service.CloseResponseBodyGracefully(resp)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}

// errorBodyLimit bounds the NovelAI error answer a converted request keeps.
const errorBodyLimit = 1 << 20

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if _, ok := relayconstant.NovelAIPath(info.RelayMode); ok {
		return openai.NovelAIImageHandler(c, info, resp)
	}
	if !isConvertedMode(info.RelayMode) || a.generation == nil {
		return nil, ErrNativeOnly(c)
	}
	switch {
	case info.RelayMode == relayconstant.RelayModeChatCompletions:
		return chatResponse(c, resp, info, a.generation)
	case info.IsStream:
		return imagesStreamResponse(c, resp, info, a.generation, a.output)
	}
	return imagesResponse(c, resp, info, a.generation, a.output)
}

// ConvertOpenAIRequest turns a chat completion into a NovelAI generation
// (see chatGenerationIntent and BuildGeneration), always a non-stream call
// to /ai/generate-image whatever the client's stream. The chat's n images
// are reserved before the send, as the image routes reserve theirs
// (service.PrepareChatImageBilling).
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	a.generation, a.streamed = nil, false
	if info.RelayMode != relayconstant.RelayModeChatCompletions {
		return nil, ErrNativeOnly(c)
	}
	intent, apiErr := chatGenerationIntent(c, info, request)
	if apiErr != nil {
		return nil, apiErr
	}
	generation, apiErr := BuildGeneration(c, intent)
	if apiErr != nil {
		return nil, apiErr
	}
	if apiErr := service.PrepareChatImageBilling(c, info, generation.NSamples); apiErr != nil {
		return nil, apiErr
	}
	a.generation = generation
	return generation.RequestBody(false), nil
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, ErrNativeOnly(c)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, ErrNativeOnly(c)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, ErrNativeOnly(c)
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, ErrNativeOnly(c)
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, ErrNativeOnly(c)
}

// ConvertImageRequest turns an OpenAI images request into NovelAI's (see
// imageGenerationIntent and BuildGeneration). The request carries no
// top-level n, so the image relay keeps the validated count as the
// reservation; n_samples equals it. A stream on a model that streams goes
// to /ai/generate-image-stream in SSE mode; V3 models answer it whole.
func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	a.generation, a.streamed = nil, false
	if !isImagesMode(info.RelayMode) {
		return nil, ErrNativeOnly(c)
	}
	intent, output, apiErr := imageGenerationIntent(c, info, &request)
	if apiErr != nil {
		return nil, apiErr
	}
	generation, apiErr := BuildGeneration(c, intent)
	if apiErr != nil {
		return nil, apiErr
	}
	a.generation, a.output = generation, output
	a.streamed = request.Stream != nil && *request.Stream && generation.Streamable
	return generation.RequestBody(a.streamed), nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, ErrNativeOnly(c)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
