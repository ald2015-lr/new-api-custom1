package novelai

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// chatGenerationIntent reads a chat completion as a NovelAI generation. The
// prompt is the text of the last user message (its text parts joined by
// newlines), never the history, which carries earlier images as base64; an
// image in that message (image_url: a data URL, base64, or an http(s) URL
// through the SSRF-protected downloader) makes it img2img. n is the number
// of images, size the canvas. The NovelAI extension keys and parameters
// come from the request's top level or its extra_body object, the top level
// first, as the chat request type keeps no unknown key.
func chatGenerationIntent(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (GenerationIntent, *types.NewAPIError) {
	var message *dto.Message
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			message = &request.Messages[i]
			break
		}
	}
	if message == nil {
		return GenerationIntent{}, badRequest(c, i18n.MsgNovelAIChatPromptMissing, nil)
	}
	var texts, images []string
	for _, part := range message.ParseContent() {
		switch part.Type {
		case dto.ContentTypeText:
			texts = append(texts, part.Text)
		case dto.ContentTypeImageURL:
			if media := part.GetImageMedia(); media != nil && media.Url != "" {
				images = append(images, media.Url)
			}
		}
	}
	if len(images) > 1 {
		return GenerationIntent{}, badRequest(c, i18n.MsgNovelAIImageMultiple, map[string]any{"Count": len(images)})
	}
	count := 1
	if request.N != nil && *request.N != 0 {
		if *request.N < 1 || *request.N > dto.MaxImageN {
			return GenerationIntent{}, badRequest(c, i18n.MsgNovelAIFieldRange, map[string]any{"Field": "n", "Min": 1, "Max": dto.MaxImageN})
		}
		count = *request.N
	}

	invalid := badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "body"})
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return GenerationIntent{}, invalid
	}
	body, err := storage.Bytes()
	if err != nil {
		return GenerationIntent{}, invalid
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(body, &fields); err != nil {
		return GenerationIntent{}, invalid
	}
	if apiErr := mergeNestedFields(c, fields, "extra_body"); apiErr != nil {
		return GenerationIntent{}, apiErr
	}
	extras, parameters, apiErr := ParseExtensions(c, fields)
	if apiErr != nil {
		return GenerationIntent{}, apiErr
	}

	intent := GenerationIntent{
		Model:       request.Model,
		Prompt:      strings.Join(texts, "\n"),
		Size:        request.Size,
		N:           count,
		ImageFormat: formatPNG,
		PresetsOff:  info.ChannelOtherSettings.NovelAIPresetsDisabled,
		Extras:      extras,
		Parameters:  parameters,
	}
	if len(images) == 1 {
		if intent.Image, apiErr = LoadImageReference(c, images[0], "image_url"); apiErr != nil {
			return GenerationIntent{}, apiErr
		}
	}
	return intent, nil
}

// chatResponse answers a chat completion from NovelAI's /ai/generate-image
// response. The images are PNGs (some chat clients drop other data URLs)
// as Markdown data-URL images: without stream, one assistant message with
// all of them; with stream, a role chunk once NovelAI answered, one content
// chunk per whole image, the stop chunk, the usage chunk unless the client
// turned stream_options.include_usage off, and [DONE]. The usage is the
// prompt estimate with no completion tokens: the images are not text, and
// nothing counts the content again. The billed quantity becomes the images
// the client received; a client that left keeps the reservation, and a
// failure after the 2xx is final and charges nothing (on a stream it is an
// error chunk).
func chatResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, generation *Generation) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	usage := generation.Usage()
	id, created, model := helper.GetResponseID(c), time.Now().Unix(), info.UpstreamModelName
	clientLeft := func(err error) (*dto.Usage, *types.NewAPIError) {
		openai.NovelAIClientLeft(info, err)
		logger.LogWarn(c, common.LogText("NovelAI response stopped before it reached the client, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
		return usage, nil
	}
	if info.IsStream {
		info.StreamStatus = relaycommon.NewStreamStatus()
		helper.SetEventStreamHeaders(c)
		c.Status(http.StatusOK)
		if err := helper.ObjectData(c, helper.GenerateStartEmptyResponse(id, created, model, nil)); err != nil {
			return clientLeft(err)
		}
	}
	images, left, apiErr := readImages(c, resp, info, generation.NSamples)
	if left {
		return usage, nil
	}
	if apiErr != nil {
		return nil, apiErr
	}
	contents := make([]string, 0, len(images))
	for _, data := range images {
		rendered, format, err := RenderImage(data, formatPNG, 0)
		if err != nil {
			return nil, noImageError(c, err)
		}
		contents = append(contents, "![image]("+DataURL(rendered, format)+")")
	}
	info.SetFirstResponseTime()

	if !info.IsStream {
		choice := dto.OpenAITextResponseChoice{Index: 0, Message: dto.Message{Role: "assistant"}, FinishReason: "stop"}
		choice.SetStringContent(strings.Join(contents, "\n\n"))
		encoded, err := common.Marshal(dto.OpenAITextResponse{
			Id: id, Object: "chat.completion", Created: created, Model: model,
			Choices: []dto.OpenAITextResponseChoice{choice}, Usage: *usage,
		})
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
		}
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		if _, err := c.Writer.Write(encoded); err != nil {
			return clientLeft(err)
		}
		info.UpdateImageCount(int64(len(images)))
		return usage, nil
	}

	for i, content := range contents {
		if i > 0 {
			content = "\n\n" + content
		}
		chunk := dto.ChatCompletionsStreamResponse{
			Id: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: &content}}},
		}
		if err := helper.ObjectData(c, chunk); err != nil {
			return clientLeft(err)
		}
	}
	info.UpdateImageCount(int64(len(images)))
	if err := helper.ObjectData(c, helper.GenerateStopResponse(id, created, model, "stop")); err != nil {
		return clientLeft(err)
	}
	if info.ShouldIncludeUsage {
		if err := helper.ObjectData(c, helper.GenerateFinalUsageResponse(id, created, model, *usage)); err != nil {
			return clientLeft(err)
		}
	}
	helper.Done(c)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	return usage, nil
}
