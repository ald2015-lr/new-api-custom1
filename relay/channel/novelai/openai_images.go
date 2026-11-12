package novelai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// ImageOutput is how an OpenAI images client wants its images.
type ImageOutput struct {
	// ResponseFormat is "b64_json" (the default) or "url", which carries a
	// data: URL: new-api hosts no files.
	ResponseFormat string
	// OutputFormat is "png" (the default), "webp" or "jpeg".
	OutputFormat string
	// Compression is the JPEG quality, 0 to 100 (100 by default).
	Compression int
	// Background is "transparent" or "opaque", as the response reports it.
	Background string
	// PartialImages is the number of partial images a stream asks for, 0
	// to 3.
	PartialImages int
	// Quality is what stream events echo as quality: the request's when it
	// is one OpenAI's events define, otherwise auto. NovelAI has no quality
	// setting.
	Quality string
	// Event names the stream events: image_generation, or image_edit on
	// /v1/images/edits.
	Event string
}

// rejectedImageFields are OpenAI image fields NovelAI has nothing for.
var rejectedImageFields = []string{"markup", "frame_count", "frame_duration_ms", "svg"}

// imageGenerationIntent reads an OpenAI images request, /v1/images/generations
// or /v1/images/edits as JSON or multipart, into a GenerationIntent and the
// output it asks for. Edits take one image (image, image[] or images[0]),
// and a mask makes them inpaint; a generation with the extension image is
// an edit. The NovelAI keys come from the top level or the extra_fields
// object, the top level first. quality, moderation, style, user,
// input_fidelity and similar fields are ignored.
func imageGenerationIntent(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ImageRequest) (GenerationIntent, ImageOutput, *types.NewAPIError) {
	var output ImageOutput
	fields, files, apiErr := imageRequestFields(c)
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if apiErr := mergeNestedFields(c, fields, "extra_fields"); apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	present := func(key string) bool {
		raw, ok := fields[key]
		return ok && !isJSONNull(raw) && string(raw) != `""`
	}
	for _, key := range rejectedImageFields {
		if present(key) || len(files[key]) > 0 {
			return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIFieldUnsupported, map[string]any{"Field": key})
		}
	}
	openAIFields := requestFields{c: c, values: fields}
	model := request.Model

	output.ResponseFormat, apiErr = openAIFields.choice("response_format", []string{"b64_json", "url"}, model)
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if output.ResponseFormat == "" {
		output.ResponseFormat = "b64_json"
	}
	output.OutputFormat, apiErr = openAIFields.choice("output_format", []string{formatPNG, formatWebP, formatJPEG}, model)
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if output.OutputFormat == "" {
		output.OutputFormat = formatPNG
	}
	output.Compression = 100
	if compression, err := openAIFields.integer("output_compression", 0, 100); err != nil {
		return GenerationIntent{}, output, err
	} else if compression != nil {
		output.Compression = int(*compression)
	}
	background, apiErr := openAIFields.choice("background", []string{"auto", "opaque", "transparent"}, model)
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	transparent := background == "transparent"
	if transparent && output.OutputFormat == formatJPEG {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAITransparentUnsupported, nil)
	}
	output.Background = "opaque"
	if transparent {
		output.Background = "transparent"
	}
	if partials, err := openAIFields.integer("partial_images", 0, 3); err != nil {
		return GenerationIntent{}, output, err
	} else if partials != nil {
		output.PartialImages = int(*partials)
	}
	output.Quality = "auto"
	if slices.Contains([]string{"low", "medium", "high"}, request.Quality) {
		output.Quality = request.Quality
	}
	output.Event = "image_generation"
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		output.Event = "image_edit"
	}

	sourceImage, images, apiErr := imageInput(c, fields, files, "image")
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if images > 1 {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIImageMultiple, map[string]any{"Count": images})
	}
	mask, masks, apiErr := imageInput(c, fields, files, "mask")
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if masks > 1 {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "mask"})
	}
	if images == 0 && (info.RelayMode == relayconstant.RelayModeImagesEdits || masks > 0) {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIImageMissing, nil)
	}

	extras, parameters, apiErr := ParseExtensions(c, fields)
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	count, err := request.ImageCount(false)
	if err != nil {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "n"})
	}
	intent := GenerationIntent{
		Model:       model,
		Prompt:      request.Prompt,
		Size:        request.Size,
		N:           count,
		ImageFormat: formatPNG,
		Transparent: transparent,
		PresetsOff:  info.ChannelOtherSettings.NovelAIPresetsDisabled,
		HDOff:       info.ChannelOtherSettings.NovelAIHDDisabled,
		Quality:     request.Quality,
		Extras:      extras,
		Parameters:  parameters,
		Image:       sourceImage,
		Mask:        mask,
	}
	if output.OutputFormat == formatWebP {
		intent.ImageFormat = formatWebP
	}
	return intent, output, nil
}

// imageRequestFields returns the top-level fields of the client's request as
// JSON, and for a multipart form its files.
func imageRequestFields(c *gin.Context) (map[string]json.RawMessage, map[string][]*multipart.FileHeader, *types.NewAPIError) {
	invalid := badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "body"})
	if strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
		form := c.Request.MultipartForm
		if form == nil {
			parsed, err := common.ParseMultipartFormReusable(c)
			if err != nil {
				return nil, nil, invalid
			}
			form = parsed
		}
		return FormFields(form.Value), form.File, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, nil, invalid
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, nil, invalid
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(body, &fields); err != nil {
		return nil, nil, invalid
	}
	return fields, nil, nil
}

// imageInput counts the images a request gives for field ("image" or
// "mask") and loads the image when there is exactly one: NovelAI takes one,
// so more are rejected before anything is read or downloaded. They are the
// multipart files named field or field[...], or in JSON the value of field
// (a URL, data URL or base64 string, an {"image_url": ...} object, or a list
// of them) and, for image, the images list of the JSON edit body. file_id
// references are rejected: there is no Files API to read them from.
func imageInput(c *gin.Context, fields map[string]json.RawMessage, files map[string][]*multipart.FileHeader, field string) ([]byte, int, *types.NewAPIError) {
	var headers []*multipart.FileHeader
	for key, list := range files {
		if key == field || strings.HasPrefix(key, field+"[") {
			headers = append(headers, list...)
		}
	}
	keys := []string{field}
	if field == "image" {
		keys = append(keys, "images")
	}
	var references []json.RawMessage
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok || isJSONNull(raw) || string(raw) == `""` {
			continue
		}
		if common.GetJsonType(raw) != "array" {
			references = append(references, raw)
			continue
		}
		var list []json.RawMessage
		if common.Unmarshal(raw, &list) != nil {
			return nil, 0, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": key})
		}
		references = append(references, list...)
	}
	count := len(headers) + len(references)
	if count != 1 {
		return nil, count, nil
	}
	if len(references) == 1 {
		data, apiErr := imageReference(c, references[0], field)
		return data, count, apiErr
	}
	invalid := invalidImage(c, field, maxInputPixels)
	file, err := headers[0].Open()
	if err != nil {
		return nil, count, invalid
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return nil, count, invalid
	}
	return data, count, nil
}

// imageReference loads one JSON image reference: a string, or an object
// with image_url (a string or {"url": ...}); file_id is rejected.
func imageReference(c *gin.Context, raw json.RawMessage, field string) ([]byte, *types.NewAPIError) {
	var reference string
	if common.Unmarshal(raw, &reference) == nil {
		return LoadImageReference(c, reference, field)
	}
	var object struct {
		ImageURL json.RawMessage `json:"image_url"`
		FileID   *string         `json:"file_id"`
	}
	if common.Unmarshal(raw, &object) != nil {
		return nil, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": field})
	}
	if object.FileID != nil {
		return nil, badRequest(c, i18n.MsgNovelAIFieldUnsupported, map[string]any{"Field": "file_id"})
	}
	if common.Unmarshal(object.ImageURL, &reference) != nil {
		var nested struct {
			URL string `json:"url"`
		}
		if common.Unmarshal(object.ImageURL, &nested) != nil || nested.URL == "" {
			return nil, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": field})
		}
		reference = nested.URL
	}
	return LoadImageReference(c, reference, field)
}

// openAIImageData is one image of an OpenAI images response. seed and size
// are extensions: the seed NovelAI generated the image with (an HD image's
// enhance seed follows from it, hdSeed, so it reproduces the HD image too),
// and the image's own size, the HD canvas or the generation's.
type openAIImageData struct {
	B64JSON string `json:"b64_json,omitempty"`
	URL     string `json:"url,omitempty"`
	Seed    int64  `json:"seed"`
	Size    string `json:"size,omitempty"`
}

// OpenAIImageUsage is the usage object of an OpenAI images response and of
// its completed stream events.
type OpenAIImageUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		TextTokens  int `json:"text_tokens"`
		ImageTokens int `json:"image_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openAIImagesResponse struct {
	Created      int64             `json:"created"`
	Data         []openAIImageData `json:"data"`
	Background   string            `json:"background"`
	OutputFormat string            `json:"output_format"`
	Size         string            `json:"size"`
	Usage        OpenAIImageUsage  `json:"usage"`
}

// ImageUsage is the OpenAI images usage (gpt-image's shape) of a
// generation's Usage.
func ImageUsage(usage *dto.Usage) OpenAIImageUsage {
	var result OpenAIImageUsage
	result.InputTokens = usage.PromptTokens
	result.InputTokensDetails.TextTokens = usage.PromptTokensDetails.TextTokens
	result.InputTokensDetails.ImageTokens = usage.PromptTokensDetails.ImageTokens
	result.OutputTokens = usage.CompletionTokens
	result.TotalTokens = usage.TotalTokens
	return result
}

// imagesResponse answers an OpenAI images request from NovelAI's
// /ai/generate-image response: each image NovelAI returned, in its order,
// rendered in the requested output format, as b64_json or a data: URL, with
// the real size and format, and the generation's usage for those images.
// With HD (e not nil) each image is enhanced in turn once every base
// rendered, and the key is free before the response is written; an image HD
// could not enhance is delivered as NovelAI made it. The usage stays the
// base canvas's, so HD costs nothing more. The billed quantity becomes the
// images the client received. Upstream accepted the job (2xx) and may have
// spent Anlas, so every failure here skips retries; one that loses the
// images charges nothing, and a client that left keeps the reserved
// quantity, its usage included.
func imagesResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, generation *Generation, output ImageOutput, e *enhancer) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	images, clientLeft, apiErr := readImages(c, resp, info, generation.NSamples)
	if clientLeft {
		return generation.Tokens.Usage(info.SettledImageCount()), nil
	}
	if apiErr != nil {
		return nil, apiErr
	}

	finals := make([]renderedImage, 0, len(images))
	for _, data := range images {
		rendered, format, err := RenderImage(data, output.OutputFormat, output.Compression)
		if err != nil {
			return nil, noImageError(c, err)
		}
		finals = append(finals, renderedImage{rendered, format, generation.Size()})
	}
	for i, data := range images {
		final, err := e.deliver(i, data, finals[i], output.OutputFormat, output.Compression, i == len(images)-1)
		if err != nil {
			openai.NovelAIClientLeft(info, err)
			logger.LogWarn(c, common.LogText("NovelAI client left before the response reached it, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
			return generation.Tokens.Usage(info.SettledImageCount()), nil
		}
		finals[i] = final
	}

	response := openAIImagesResponse{
		Created:    time.Now().Unix(),
		Background: output.Background,
		Size:       finals[0].size,
		Usage:      ImageUsage(generation.Tokens.Usage(len(images))),
	}
	for i, final := range finals {
		if response.OutputFormat == "" {
			response.OutputFormat = final.format
		}
		item := openAIImageData{Seed: generation.Seed + int64(i), Size: final.size}
		if output.ResponseFormat == "url" {
			item.URL = DataURL(final.data, final.format)
		} else {
			item.B64JSON = base64.StdEncoding.EncodeToString(final.data)
		}
		response.Data = append(response.Data, item)
	}
	encoded, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}
	info.SetFirstResponseTime()
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if _, err := c.Writer.Write(encoded); err != nil {
		openai.NovelAIClientLeft(info, err)
		logger.LogWarn(c, common.LogText("NovelAI response stopped before it reached the client, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
		return generation.Tokens.Usage(info.SettledImageCount()), nil
	}
	info.UpdateImageCount(int64(len(images)))
	return generation.Tokens.Usage(info.SettledImageCount()), nil
}

// readImages reads the images of a complete /ai/generate-image response, at
// most samples: the n_samples asked for, which billing reserved, so an
// answer with more raises neither what is delivered nor what is billed. A
// read the client cut short by leaving reports clientLeft, which keeps the
// reserved quantity and is no perf-metrics sample; any other failure is a
// final 502, as upstream accepted the job (2xx) and may have spent Anlas on
// it.
func readImages(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, samples int) (images [][]byte, clientLeft bool, apiErr *types.NewAPIError) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(responseLimit)+1))
	if err != nil {
		if c.Request.Context().Err() != nil {
			openai.NovelAIClientLeft(info, err)
			logger.LogWarn(c, common.LogText("NovelAI client left before the response reached it, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
			return nil, true, nil
		}
		return nil, false, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if len(body) > responseLimit {
		return nil, false, noImageError(c, errors.New("the response exceeds the limit"))
	}
	images, err = ParseImages(body)
	if err != nil || len(images) == 0 {
		return nil, false, noImageError(c, err)
	}
	if len(images) > samples {
		logger.LogWarn(c, common.LogText("NovelAI returned %d images for %d asked for, keeping %d", len(images), samples, samples))
		images = images[:samples]
	}
	return images, false, nil
}

// noImageError is the final 502 of a 2xx NovelAI answer the relay could not
// turn into images; the cause goes to the log.
func noImageError(c *gin.Context, cause error) *types.NewAPIError {
	if cause != nil {
		logger.LogWarn(c, common.LogText("NovelAI response could not be read as images: %s", cause.Error()))
	}
	return types.NewOpenAIError(errors.New(i18n.T(c, i18n.MsgNovelAINoImage)), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}
