package novelai

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
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
// A converted text-to-image request on V4 and later also gets the HD pass
// (enhance.go) unless the channel or the request turns it off: each image
// enhanced at 1.5 times its size, at the same price. A channel can also turn
// on NovelAI's 2x upscale (upscale.go), which follows HD image by image.
//
// Every other route fails with a 400. On NovelAI's own host the generations
// of one key run one at a time, and the others wait in line for a key of the
// channel (holdGeneration).
type Adaptor struct {
	// generation describes the converted request, set by
	// ConvertImageRequest or ConvertOpenAIRequest; output is how an images
	// client wants its images, and streamed whether NovelAI streams them.
	generation *Generation
	output     ImageOutput
	streamed   bool
	// hd is the HD pass DoRequest planned from the sent body, nil without
	// one; hdBlocked is why it will not call NovelAI although planned (a 402
	// put the key in its cooldown). releaseSlot is the key's release while
	// HD keeps the key past the base answer. hdResult is what the pass did,
	// for the consume log.
	hd          *hdPlan
	hdBlocked   string
	releaseSlot func()
	hdResult    hdOutcome
	// upscale tells that DoRequest planned the upscale, which keeps the key
	// past the base answer too; upscaleResult is what it did.
	upscale       bool
	upscaleResult upscaleResult
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
	return imageBaseURL(info.ChannelBaseUrl) + path, nil
}

// imageBaseURL is the base URL of a NovelAI channel's image calls: the
// channel's, and NovelAI's image host for an empty one.
func imageBaseURL(channelBaseURL string) string {
	baseURL := strings.TrimSuffix(channelBaseURL, "/")
	if baseURL == "" {
		return constant.GetChannelBaseURL(constant.ChannelTypeNovelAI)
	}
	return baseURL
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

// DoRequest sends the request once its turn for a key of the channel came on
// NovelAI's own host (holdGeneration), and records NovelAI's answer for that
// key (noteGenerationAnswer). A send that failed after it may have reached
// NovelAI is final: NovelAI may have spent Anlas on it.
//
// A converted request that wants HD plans it from the body as sent (planHD).
// HD and the upscale keep the key past the base answer: their calls are the
// request's next generations on it, and DoResponse's enhancer releases it.
// A base answer other than 200 (201 is NovelAI's 200) drops both and frees
// the key at once, as the relay does not call DoResponse for it.
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
	a.hd, a.hdBlocked, a.releaseSlot, a.hdResult = nil, "", nil, hdOutcome{}
	a.upscale, a.upscaleResult = false, upscaleResult{}
	if a.generation != nil && isConvertedMode(info.RelayMode) {
		a.upscale = a.generation.UpscalePlanned()
		if a.generation.Upscale && !a.upscale {
			logger.LogDebug(c, "NovelAI upscale skipped: %s", a.generation.UpscaleExcluded)
		}
	}
	if a.generation != nil && a.generation.HD && isConvertedMode(info.RelayMode) {
		var skipped string
		if a.hd, skipped = planHD(requestBody, a.generation); a.hd == nil {
			logger.LogDebug(c, "NovelAI HD skipped: %s", skipped)
		}
	} else if a.generation != nil && a.generation.hdSkip != "" {
		logger.LogDebug(c, "NovelAI HD skipped: %s", a.generation.hdSkip)
	}
	release, apiErr := holdGeneration(c, info)
	if apiErr != nil {
		a.hd, a.upscale = nil, false
		return nil, apiErr
	}
	if a.hd != nil {
		// The key the queue chose.
		if refusal, refused := hdRefused(info.ApiKey); refused {
			a.hdBlocked = refusal.reason()
		}
	}
	holdsKey := a.hd != nil && a.hdBlocked == "" || a.upscale
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	if err != nil {
		release()
		a.hd, a.upscale = nil, false
		if RequestMayHaveReachedUpstream(err) {
			return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
		}
		return nil, err
	}
	if holdsKey {
		a.releaseSlot = release
	}
	if isNovelAIImageHost(info.ChannelBaseUrl) {
		noteGenerationAnswer(c, info, resp.StatusCode)
		// The key is free for the next in line once NovelAI's answer
		// arrived, whatever the client still has to receive; with HD or the
		// upscale, once their last answer arrived.
		bodyRelease := release
		if holdsKey {
			bodyRelease = func() {}
		}
		resp.Body = newGenerationBody(resp.Body, bodyRelease)
	}
	if !isConvertedMode(info.RelayMode) {
		return resp, nil
	}
	if resp.StatusCode == http.StatusCreated {
		// NovelAI's documented success status; the image and chat relays
		// take 200.
		resp.StatusCode = http.StatusOK
	}
	if resp.StatusCode != http.StatusOK {
		a.dropPasses()
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// The image and chat relays read an error answer whole, and leave it
		// open when the read fails: it is read here, bounded as on the
		// native routes, and closed, which frees the key for a retry.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		service.CloseResponseBodyGracefully(resp)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}

// errorBodyLimit bounds the NovelAI error answer a converted request keeps.
const errorBodyLimit = 1 << 20

// dropPasses gives up the HD pass and the upscale of a request whose base
// answer the relay does not take, and frees the key they kept.
func (a *Adaptor) dropPasses() {
	if a.releaseSlot != nil {
		a.releaseSlot()
	}
	a.hd, a.hdBlocked, a.releaseSlot, a.upscale = nil, "", nil, false
}

// DoResponse answers a converted request from NovelAI's base answer; the
// planned HD pass and upscale run in it, image by image (enhancer), and free
// the key once their last call answered, at the latest when DoResponse
// returns. Every image of a request the upscale excludes as a whole counts
// as not applicable.
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if _, ok := relayconstant.NovelAIPath(info.RelayMode); ok {
		return openai.NovelAIImageHandler(c, info, resp)
	}
	if !isConvertedMode(info.RelayMode) || a.generation == nil {
		return nil, ErrNativeOnly(c)
	}
	var e *enhancer
	if a.hd != nil || a.upscale {
		e = &enhancer{c: c, info: info, adaptor: a, plan: a.hd, release: func() {}, stopped: a.hdBlocked}
		if a.releaseSlot != nil {
			e.release = a.releaseSlot
		}
		if a.upscale {
			e.upscale = &upscaler{c: c, info: info, total: a.generation.NSamples}
		}
		defer e.finish()
	}
	var result *dto.Usage
	switch {
	case info.RelayMode == relayconstant.RelayModeChatCompletions:
		result, err = chatResponse(c, resp, info, a.generation, e)
	case info.IsStream:
		result, err = imagesStreamResponse(c, resp, info, a.generation, a.output, e)
	default:
		result, err = imagesResponse(c, resp, info, a.generation, a.output, e)
	}
	if a.generation.Upscale && a.generation.UpscaleExcluded != "" && err == nil {
		a.upscaleResult = upscaleResult{info: info}
		a.upscaleResult.counts[upscaleNotApplicable] = info.SettledImageCount()
	}
	return result, err
}

// ConsumeLogContent is what the consume log says about the HD pass and the
// upscale. HD: the HD canvas when every image was enhanced, how many were
// when some were, and that HD was unavailable when it was due and none was
// (delivered at the standard size, unless the upscale enlarged some image).
// The upscale, when the channel has it on and the request did not opt out:
// how many images were upscaled, and how many were not for want of Anlas,
// for not qualifying, or for a failure. Nothing for a pass that was not due,
// or once the client left. HD's reason stays in the admins' diagnostics, the
// upscale's in the server log: the account's Anlas are the operator's
// business.
func (a *Adaptor) ConsumeLogContent() []*common.Message {
	var content []*common.Message
	result := a.hdResult
	switch {
	case result.info == nil || result.delivered == 0 || clientLeftRequest(result.info):
	case result.enhanced == 0 && a.upscaleResult.counts[upscaleApplied] > 0:
		content = append(content, common.NewMessage("HD unavailable"))
	case result.enhanced == 0:
		content = append(content, common.NewMessage("HD unavailable, delivered at the standard size"))
	case result.enhanced == result.delivered:
		content = append(content, common.NewMessage("HD {{size}}", map[string]any{"size": result.canvas}))
	default:
		content = append(content, common.NewMessage("HD {{size}} for {{count}} of {{total}} images", map[string]any{"size": result.canvas, "count": result.enhanced, "total": result.delivered}))
	}
	return append(content, a.upscaleResult.notes()...)
}

// clientLeftRequest tells that the client left before its response reached
// it.
func clientLeftRequest(info *relaycommon.RelayInfo) bool {
	return info.StreamStatus != nil && info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone
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

// CountRequest counts the converted request's tokens again from requestJSON,
// the body the channel's parameter override made of the one BuildGeneration
// built. NovelAI generates what it receives, so the usage the response
// reports and the request is billed at follows an override of the canvas,
// the prompts or the reference images (RequestTokens reads neither the image
// nor the mask, only the action).
func (a *Adaptor) CountRequest(requestJSON []byte) {
	if a.generation != nil {
		a.generation.Tokens = RequestTokens(requestJSON)
	}
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
