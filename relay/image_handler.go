package relay

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/novelai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/requestplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

// consumeLogContentAdaptor is an adaptor that adds what it did to the
// consume log's content, after the request's own lines and before the image
// count (a NovelAI channel's HD pass).
type consumeLogContentAdaptor interface{ ConsumeLogContent() []*common.Message }

// outboundImageCounter is an image adaptor whose upstream body carries the
// image count in a member other than n. OutboundImageCount reads the count
// from that body, nil when the member is absent.
type outboundImageCounter interface {
	OutboundImageCount(body []byte) (*uint, error)
}

func ImageHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	info.BillingImageCount = nil
	info.ImageRequestCount = 0

	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.ImageRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(imageReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ImageRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	imageCount, err := request.ImageCount(false)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	var requestBody io.Reader
	var jsonData []byte
	var outbound struct {
		N *uint `json:"n"`
	}
	validateQuantity := false

	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if newAPIError = requestplugin.PassThrough(c, info, storage); newAPIError != nil {
			return newAPIError
		}
		if !strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
			counter, countsOwnMember := adaptor.(outboundImageCounter)
			switch {
			case countsOwnMember:
				// The adaptor sends the count in its own member, which only
				// it can read, so it reads the whole body.
				var raw []byte
				raw, err = storage.Bytes()
				if err == nil {
					err = common.Unmarshal(raw, &outbound)
				}
				if err == nil {
					outbound.N, err = counter.OutboundImageCount(raw)
				}
			case storage.IsDisk():
				reader, readErr := storage.NewReader()
				if readErr != nil {
					return types.NewErrorWithStatusCode(readErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
				err = common.DecodeJsonBody(reader, &outbound)
				_ = reader.Close()
			default:
				var raw []byte
				raw, err = storage.Bytes()
				if err == nil {
					err = common.Unmarshal(raw, &outbound)
				}
			}
			if err != nil {
				return types.NewErrorWithStatusCode(fmt.Errorf("invalid image billing parameters: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			validateQuantity = true
			if common.DebugEnabled {
				if raw, readErr := storage.Bytes(); readErr == nil {
					logger.LogDebug(c, "image request body: %s", raw)
				}
			}
		}
		requestBody = common.NewReplayableBodyReader(storage)
	} else {
		convertedRequest, err := adaptor.ConvertImageRequest(c, info, *request)
		if err != nil {
			// An adaptor that already classified its rejection (status code
			// and retry policy) keeps that classification instead of being
			// downgraded to a retryable conversion failure.
			var apiErr *types.NewAPIError
			if errors.As(err, &apiErr) {
				return apiErr
			}
			return types.NewError(err, types.ErrorCodeConvertRequestFailed)
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		switch convertedRequest.(type) {
		case *bytes.Buffer:
			if newAPIError = requestplugin.PassThrough(c, info, nil); newAPIError != nil {
				return newAPIError
			}
			requestBody = convertedRequest.(io.Reader)
		default:
			jsonData, err = common.Marshal(convertedRequest)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}

			built := jsonData
			if jsonData, newAPIError = requestplugin.Outbound(c, info, jsonData); newAPIError != nil {
				return newAPIError
			}
			// apply param override
			if len(info.ParamOverride) > 0 {
				jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
				if err != nil {
					return newAPIErrorFromParamOverride(err)
				}
			}
			if apiErr := checkNovelAIOverride(info, adaptor, built, jsonData, imageCount); apiErr != nil {
				return apiErr
			}
			if newAPIError = requestplugin.Egress(c, info, jsonData); newAPIError != nil {
				return newAPIError
			}

		}
	}
	if jsonData != nil {
		// This is a different trust boundary from ingress: channel overrides
		// and request plugins can change the quantity actually submitted, in
		// n or in the member the adaptor sends it in.
		err := common.Unmarshal(jsonData, &outbound)
		if counter, ok := adaptor.(outboundImageCounter); ok && err == nil {
			outbound.N, err = counter.OutboundImageCount(jsonData)
		}
		if err != nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("invalid image billing parameters: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		validateQuantity = true
	}
	if validateQuantity {
		// Validate the count in the body sent upstream, including channel overrides.
		quantityRequest := dto.ImageRequest{N: outbound.N}
		if quantityRequest.N == nil {
			quantityRequest.N = common.GetPointer(uint(imageCount))
		}
		imageCount, err = quantityRequest.ImageCount(false)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	if jsonData != nil {
		logger.LogDebug(c, "image request body: %s", jsonData)
		body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		requestBody = body
	}

	if billingErr := service.PrepareImageBillingForRequest(c, info, imageCount); billingErr != nil {
		return billingErr
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			if httpResp.StatusCode == http.StatusCreated && info.ApiType == constant.APITypeReplicate {
				// replicate channel returns 201 Created when using Prefer: wait, treat it as success.
				httpResp.StatusCode = http.StatusOK
			} else {
				newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
				// reset status code 重置状态码
				service.ResetStatusCode(newAPIError, statusCodeMappingStr)
				return newAPIError
			}
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	imageN := info.SettledImageCount()

	if usage.(*dto.Usage).TotalTokens == 0 {
		usage.(*dto.Usage).TotalTokens = 1
	}
	if usage.(*dto.Usage).PromptTokens == 0 {
		usage.(*dto.Usage).PromptTokens = 1
	}

	quality := request.Quality
	if quality == "" {
		quality = "standard"
	}

	var logContent []*common.Message

	if len(request.Size) > 0 {
		logContent = append(logContent, common.NewMessage("Size {{size}}", map[string]any{"size": request.Size}))
	}
	if len(quality) > 0 {
		logContent = append(logContent, common.NewMessage("Quality {{quality}}", map[string]any{"quality": quality}))
	}
	if a, ok := adaptor.(consumeLogContentAdaptor); ok {
		logContent = append(logContent, a.ConsumeLogContent()...)
	}
	if imageN > 0 {
		logContent = append(logContent, common.NewMessage("Image count {{count}}", map[string]any{"count": imageN}))
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), logContent)
	return nil
}

// checkNovelAIOverride settles a converted NovelAI request (image or chat)
// once the request plugins' outbound stage and the channel's parameter
// override, which run after the request was built (built) and its images
// reserved, have applied (jsonData). NovelAI generates parameters.n_samples
// images: an override or a plugin that changes it would send work nobody
// reserved, so the request is rejected before the send, as a failed override
// is. Any other change applies, and the request's tokens are counted again
// from the changed body (novelai.Adaptor.CountRequest), which is what NovelAI
// receives, so a change of the canvas or the prompts is billed and logged as
// generated. A body neither stage touched is left as built.
func checkNovelAIOverride(info *relaycommon.RelayInfo, adaptor channel.Adaptor, built, jsonData []byte, reserved int) *types.NewAPIError {
	novelAIAdaptor, ok := adaptor.(*novelai.Adaptor)
	if info.ChannelType != constant.ChannelTypeNovelAI || !ok {
		return nil
	}
	if len(info.ParamOverride) == 0 && bytes.Equal(built, jsonData) {
		return nil
	}
	samples := ojson.GetBytes(jsonData, "parameters.n_samples")
	if samples.Type == ojson.Number && samples.Num == float64(reserved) {
		novelAIAdaptor.CountRequest(jsonData)
		return nil
	}
	return types.NewErrorWithStatusCode(fmt.Errorf("the channel's parameter override or a request plugin sets parameters.n_samples to %s; it must stay %d, the images the request reserved", cmp.Or(samples.Raw, "nothing"), reserved), types.ErrorCodeChannelParamOverrideInvalid, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}
