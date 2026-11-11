package novelai

import (
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
// OpenAI images requests (/v1/images/generations and /v1/images/edits) are
// converted: BuildGeneration makes the NovelAI request, /ai/generate-image
// answers it, and the images come back in the OpenAI response shape, billed
// per image received.
//
// Every other route fails with a 400. On NovelAI's own host the generations
// of one key run one at a time (holdGeneration).
type Adaptor struct {
	// generation and output describe the converted request of an OpenAI
	// images call, set by ConvertImageRequest.
	generation *Generation
	output     ImageOutput
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

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	path, ok := relayconstant.NovelAIPath(info.RelayMode)
	if !ok {
		if !isImagesMode(info.RelayMode) || a.generation == nil {
			return "", ErrNativeOnly(nil)
		}
		path = generateImagePath
	}
	baseURL := strings.TrimSuffix(info.ChannelBaseUrl, "/")
	if baseURL == "" {
		baseURL = constant.GetChannelBaseURL(constant.ChannelTypeNovelAI)
	}
	return baseURL + path, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if isImagesMode(info.RelayMode) {
		// The converted request is JSON, and NovelAI answers it with its
		// default zip; a client's Accept (the OpenAI SDK sends
		// application/json) would ask for its JSON mode instead.
		header.Set("Content-Type", "application/json")
		header.Del("Accept")
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
		if !isImagesMode(info.RelayMode) {
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
	resp.Body = &generationBody{ReadCloser: resp.Body, release: release}
	if isImagesMode(info.RelayMode) && resp.StatusCode == http.StatusCreated {
		// NovelAI's documented success status; the image relay takes 200.
		resp.StatusCode = http.StatusOK
	}
	return resp, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if _, ok := relayconstant.NovelAIPath(info.RelayMode); ok {
		return openai.NovelAIImageHandler(c, info, resp)
	}
	if !isImagesMode(info.RelayMode) || a.generation == nil {
		return nil, ErrNativeOnly(c)
	}
	return imagesResponse(c, resp, info, a.generation, a.output)
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, ErrNativeOnly(c)
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
// reservation; n_samples equals it.
func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	a.generation = nil
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
	return generation.RequestBody(false), nil
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
