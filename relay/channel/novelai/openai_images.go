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
// an edit. quality, moderation, style, user, input_fidelity and similar
// fields are ignored.
func imageGenerationIntent(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ImageRequest) (GenerationIntent, ImageOutput, *types.NewAPIError) {
	var output ImageOutput
	fields, files, apiErr := imageRequestFields(c)
	if apiErr != nil {
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

	images, apiErr := imageInputs(c, fields, files, "image")
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	masks, apiErr := imageInputs(c, fields, files, "mask")
	if apiErr != nil {
		return GenerationIntent{}, output, apiErr
	}
	if len(images) > 1 {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIImageMultiple, map[string]any{"Count": len(images)})
	}
	if len(masks) > 1 {
		return GenerationIntent{}, output, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "mask"})
	}
	if len(images) == 0 && (info.RelayMode == relayconstant.RelayModeImagesEdits || len(masks) > 0) {
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
		Extras:      extras,
		Parameters:  parameters,
	}
	if output.OutputFormat == formatWebP {
		intent.ImageFormat = formatWebP
	}
	if len(images) == 1 {
		intent.Image = images[0]
	}
	if len(masks) == 1 {
		intent.Mask = masks[0]
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

// imageInputs loads the images a request gives for field ("image" or
// "mask"): multipart files named field or field[...] and form values, or in
// JSON the value of field (a URL, data URL or base64 string, an
// {"image_url": ...} object, or a list of them) and, for image, the
// images list of the JSON edit body. file_id references are rejected: there
// is no Files API to read them from.
func imageInputs(c *gin.Context, fields map[string]json.RawMessage, files map[string][]*multipart.FileHeader, field string) ([][]byte, *types.NewAPIError) {
	var images [][]byte
	for key, headers := range files {
		if key != field && !strings.HasPrefix(key, field+"[") {
			continue
		}
		for _, header := range headers {
			file, err := header.Open()
			if err != nil {
				return nil, badRequest(c, i18n.MsgNovelAIImageInvalid, map[string]any{"Field": field, "Max": maxInputPixels})
			}
			data, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil || len(data) == 0 {
				return nil, badRequest(c, i18n.MsgNovelAIImageInvalid, map[string]any{"Field": field, "Max": maxInputPixels})
			}
			images = append(images, data)
		}
	}
	keys := []string{field}
	if field == "image" {
		keys = append(keys, "images")
	}
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok || isJSONNull(raw) || string(raw) == `""` {
			continue
		}
		var references []json.RawMessage
		if common.GetJsonType(raw) == "array" {
			if common.Unmarshal(raw, &references) != nil {
				return nil, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": key})
			}
		} else {
			references = []json.RawMessage{raw}
		}
		for _, reference := range references {
			data, apiErr := imageReference(c, reference, field)
			if apiErr != nil {
				return nil, apiErr
			}
			images = append(images, data)
		}
	}
	return images, nil
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

// openAIImageData is one image of an OpenAI images response. seed is an
// extension: the seed NovelAI generated the image with.
type openAIImageData struct {
	B64JSON string `json:"b64_json,omitempty"`
	URL     string `json:"url,omitempty"`
	Seed    int64  `json:"seed"`
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

// ImageUsage is the OpenAI images usage of a generation's Usage.
func ImageUsage(usage *dto.Usage) OpenAIImageUsage {
	var result OpenAIImageUsage
	result.InputTokens = usage.PromptTokens
	result.InputTokensDetails.TextTokens = usage.PromptTokens
	result.TotalTokens = usage.PromptTokens
	return result
}

// imagesResponse answers an OpenAI images request from NovelAI's
// /ai/generate-image response: each image NovelAI returned, in its order,
// rendered in the requested output format, as b64_json or a data: URL, with
// the real size and format, and the prompt estimate as usage. The billed
// quantity becomes the images the client received. Upstream accepted the
// job (2xx) and may have spent Anlas, so every failure here skips retries;
// one that loses the images charges nothing, and a client that left keeps
// the reserved quantity.
func imagesResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, generation *Generation, output ImageOutput) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	usage := generation.Usage()
	images, clientLeft, apiErr := readImages(c, resp, info)
	if clientLeft {
		return usage, nil
	}
	if apiErr != nil {
		return nil, apiErr
	}

	response := openAIImagesResponse{
		Created:    time.Now().Unix(),
		Background: output.Background,
		Size:       generation.Size(),
		Usage:      ImageUsage(usage),
	}
	for i, data := range images {
		rendered, format, err := RenderImage(data, output.OutputFormat, output.Compression)
		if err != nil {
			return nil, noImageError(c, err)
		}
		if response.OutputFormat == "" {
			response.OutputFormat = format
		}
		item := openAIImageData{Seed: generation.Seed + int64(i)}
		if output.ResponseFormat == "url" {
			item.URL = DataURL(rendered, format)
		} else {
			item.B64JSON = base64.StdEncoding.EncodeToString(rendered)
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
		logger.LogWarn(c, common.LogText("NovelAI response stopped before it reached the client, billing the reserved %d images: %s", info.RequestedImageCount(), err.Error()))
		return usage, nil
	}
	info.UpdateImageCount(int64(len(images)))
	return usage, nil
}

// readImages reads the images of a complete /ai/generate-image response. A
// read the client cut short by leaving reports clientLeft, which keeps the
// reserved quantity; any other failure is a final 502, as upstream accepted
// the job (2xx) and may have spent Anlas on it.
func readImages(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (images [][]byte, clientLeft bool, apiErr *types.NewAPIError) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(responseLimit)+1))
	if err != nil {
		if c.Request.Context().Err() != nil {
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
