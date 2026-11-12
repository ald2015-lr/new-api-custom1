package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relay/channel/novelai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// novelAIErrorBodyLimit bounds a failed upstream response kept for the
// client; a longer one reaches the client as a NovelAI-shaped error instead.
const novelAIErrorBodyLimit = 1 << 20

// NovelAIUpstreamError keeps a failed upstream response inside the relay
// error, so that when it is the final attempt's failure the NovelAI client
// receives the upstream status, Content-Type and body as sent.
type NovelAIUpstreamError struct {
	Err         error
	ContentType string
	Body        []byte
}

func (e *NovelAIUpstreamError) Error() string { return e.Err.Error() }

func (e *NovelAIUpstreamError) Unwrap() error { return e.Err }

// NovelAIImageHelper relays the NovelAI native generate-image endpoints, to a
// NovelAI channel (NovelAI's official API) or to an OpenAI-type channel whose
// base URL is a NovelAI-compatible forwarder. The client body (JSON, or
// multipart with its original boundary) goes to the channel base URL + the
// NovelAI path byte for byte, so model mapping and parameter overrides do not
// apply. The call is billed like an OpenAI image request: parameters.n_samples
// is reserved as the image count before each attempt, and a successful
// response settles at the images it carries. NovelAI reports no usage, so
// the tokens are counted from the request (novelai.RequestTokens) for the
// images the call settles at.
//
// Another channel is tried only when this one answered with an error status
// (under the usual retry policy) or provably received nothing; once the
// request may have reached NovelAI the attempt is final.
func NovelAIImageHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	info.BillingImageCount = nil
	info.ImageRequestCount = 0
	// Set again once upstream answered: a stream flag here would make the
	// transport write SSE headers and pings into a binary response.
	info.IsStream = false

	// A NovelAI channel, or the OpenAI adaptor on a plain base URL (a
	// NovelAI-compatible forwarder), builds the NovelAI path.
	forwarder := info.ApiType == constant.APITypeOpenAI && info.ChannelType != constant.ChannelTypeAzure && info.ChannelType != constant.ChannelTypeCustom
	if info.ApiType != constant.APITypeNovelAI && !forwarder {
		// Retryable: another channel of the model may serve it.
		return types.NewError(errors.New(i18n.T(c, i18n.MsgNovelAIChannelUnsupported)), types.ErrorCodeInvalidRequest)
	}

	request, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.ImageRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	imageCount, err := request.ImageCount(false)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	requestJSON, err := helper.NovelAIRequestJSON(c)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	tokens := novelai.RequestTokens(requestJSON)
	if billingErr := service.PrepareImageBillingForRequest(c, info, imageCount); billingErr != nil {
		return billingErr
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	// NovelAI has no idempotency guarantee: a request that may have reached
	// it may already have spent Anlas, so it is never sent a second time.
	// Only a failure that proves nothing was sent stays retryable.
	resp, err := adaptor.DoRequest(c, info, common.NewReplayableBodyReader(storage))
	if err != nil {
		apiErr := types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
		if novelai.RequestMayHaveReachedUpstream(err) {
			types.ErrOptionWithSkipRetry()(apiErr)
		}
		return apiErr
	}
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return types.NewOpenAIError(errors.New("invalid http response"), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, novelAIErrorBodyLimit+1))
		service.CloseResponseBodyGracefully(httpResp)
		httpResp.Body = io.NopCloser(bytes.NewReader(body))
		apiErr := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
		if readErr == nil && len(body) <= novelAIErrorBodyLimit {
			apiErr.Err = &NovelAIUpstreamError{Err: apiErr.Err, ContentType: httpResp.Header.Get("Content-Type"), Body: body}
		}
		service.ResetStatusCode(apiErr, statusCodeMappingStr)
		return apiErr
	}

	info.IsStream = info.RelayMode == relayconstant.RelayModeNovelAIGenerateImageStream
	if _, apiErr := adaptor.DoResponse(c, httpResp, info); apiErr != nil {
		// Upstream accepted the job (2xx) and may have spent Anlas on it,
		// even though its response never reached the client.
		types.ErrOptionWithSkipRetry()(apiErr)
		service.ResetStatusCode(apiErr, statusCodeMappingStr)
		return apiErr
	}
	// The text tokens are at least 1, so the usage is never empty and a
	// per-image price settles, as for OpenAI images.
	images := info.SettledImageCount()
	logContent := []*common.Message{common.NewMessage("Image count {{count}}", map[string]any{"count": images})}
	service.PostTextConsumeQuota(c, info, tokens.Usage(images), logContent)
	return nil
}
