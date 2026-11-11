package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

func GetAndValidateRequest(c *gin.Context, format types.RelayFormat) (request dto.Request, err error) {
	relayMode := relayconstant.Path2RelayMode(c.Request.URL.Path)

	switch format {
	case types.RelayFormatOpenAI:
		request, err = GetAndValidateTextRequest(c, relayMode)
	case types.RelayFormatGemini:
		if strings.Contains(c.Request.URL.Path, ":embedContent") {
			request, err = GetAndValidateGeminiEmbeddingRequest(c)
		} else if strings.Contains(c.Request.URL.Path, ":batchEmbedContents") {
			request, err = GetAndValidateGeminiBatchEmbeddingRequest(c)
		} else {
			request, err = GetAndValidateGeminiRequest(c)
		}
	case types.RelayFormatClaude:
		request, err = GetAndValidateClaudeRequest(c)
	case types.RelayFormatOpenAIResponses:
		request, err = GetAndValidateResponsesRequest(c)
	case types.RelayFormatOpenAIResponsesCompaction:
		request, err = GetAndValidateResponsesCompactionRequest(c)
	case types.RelayFormatOpenAIAlphaSearch:
		request, err = GetAndValidateAlphaSearchRequest(c)
	case types.RelayFormatDeepSeekTools:
		request, err = GetAndValidateDeepSeekToolsRequest(c)

	case types.RelayFormatOpenAIImage:
		request, err = GetAndValidOpenAIImageRequest(c, relayMode)
	case types.RelayFormatEmbedding:
		request, err = GetAndValidateEmbeddingRequest(c, relayMode)
	case types.RelayFormatRerank:
		request, err = GetAndValidateRerankRequest(c)
	case types.RelayFormatOpenAIAudio:
		request, err = GetAndValidAudioRequest(c, relayMode)
	case types.RelayFormatOpenAIRealtime:
		request = &dto.BaseRequest{}
	case relayconstant.RelayFormatNovelAI:
		request, err = GetAndValidateNovelAIImageRequest(c)
	default:
		return nil, fmt.Errorf("unsupported relay format: %s", format)
	}
	return request, err
}

func GetAndValidAudioRequest(c *gin.Context, relayMode int) (*dto.AudioRequest, error) {
	audioRequest := &dto.AudioRequest{}
	err := common.UnmarshalBodyReusable(c, audioRequest)
	if err != nil {
		return nil, err
	}
	switch relayMode {
	case relayconstant.RelayModeAudioSpeech:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
	default:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
		if audioRequest.ResponseFormat == "" {
			audioRequest.ResponseFormat = "json"
		}
	}
	return audioRequest, nil
}

func GetAndValidateRerankRequest(c *gin.Context) (*dto.RerankRequest, error) {
	var rerankRequest *dto.RerankRequest
	err := common.UnmarshalBodyReusable(c, &rerankRequest)
	if err != nil {
		logger.LogError(c, common.LogText("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if rerankRequest.Query == "" {
		return nil, types.NewError(fmt.Errorf("query is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if len(rerankRequest.Documents) == 0 {
		return nil, types.NewError(fmt.Errorf("documents is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return rerankRequest, nil
}

func GetAndValidateEmbeddingRequest(c *gin.Context, relayMode int) (*dto.EmbeddingRequest, error) {
	var embeddingRequest *dto.EmbeddingRequest
	err := common.UnmarshalBodyReusable(c, &embeddingRequest)
	if err != nil {
		logger.LogError(c, common.LogText("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if embeddingRequest.Input == nil {
		return nil, fmt.Errorf("input is empty")
	}
	if relayMode == relayconstant.RelayModeModerations && embeddingRequest.Model == "" {
		embeddingRequest.Model = "omni-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && embeddingRequest.Model == "" {
		embeddingRequest.Model = c.Param("model")
	}
	return embeddingRequest, nil
}

// maxTokensLimit bounds user-supplied max token fields. These values feed
// pre-consume quota math (preConsumedTokens * ratio); an unbounded value can
// overflow the conversion and corrupt billing.
const maxTokensLimit = math.MaxInt32 / 2

// ExceedsMaxTokensLimit checks token limits before they reach billing arithmetic.
func ExceedsMaxTokensLimit(values ...*uint) bool {
	for _, v := range values {
		if lo.FromPtrOr(v, uint(0)) > maxTokensLimit {
			return true
		}
	}
	return false
}

func GetAndValidateResponsesRequest(c *gin.Context) (*dto.OpenAIResponsesRequest, error) {
	request := &dto.OpenAIResponsesRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	if request.Input == nil {
		return nil, errors.New("input is required")
	}
	if ExceedsMaxTokensLimit(request.MaxOutputTokens) {
		return nil, errors.New("max_output_tokens is invalid")
	}
	return request, nil
}

func GetAndValidateAlphaSearchRequest(c *gin.Context) (*dto.AlphaSearchRequest, error) {
	request := &dto.AlphaSearchRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	rawBody, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	request.RawBody = rawBody
	return request, nil
}

// GetAndValidateDeepSeekToolsRequest keeps the raw body for forwarding. The
// upstream validates query/url itself, and no field changes the per-call price.
func GetAndValidateDeepSeekToolsRequest(c *gin.Context) (*dto.DeepSeekToolsRequest, error) {
	request := &dto.DeepSeekToolsRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	rawBody, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	if len(rawBody) == 0 {
		return nil, errors.New("request body is empty")
	}
	request.RawBody = rawBody
	return request, nil
}

func GetAndValidateResponsesCompactionRequest(c *gin.Context) (*dto.OpenAIResponsesCompactionRequest, error) {
	request := &dto.OpenAIResponsesCompactionRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	return request, nil
}

func GetAndValidOpenAIImageRequest(c *gin.Context, relayMode int) (*dto.ImageRequest, error) {
	imageRequest := &dto.ImageRequest{}

	switch relayMode {
	case relayconstant.RelayModeImagesEdits:
		if strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
			form, err := common.ParseMultipartFormReusable(c)
			if err != nil {
				return nil, fmt.Errorf("failed to parse image edit form request: %w", err)
			}
			formData := url.Values(form.Value)
			c.Request.MultipartForm = form
			c.Request.PostForm = formData
			imageRequest.Prompt = formData.Get("prompt")
			imageRequest.Model = formData.Get("model")
			if nValue := strings.TrimSpace(formData.Get("n")); nValue != "" {
				n, err := strconv.Atoi(nValue)
				if err != nil || n < 0 || n > dto.MaxImageN {
					return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
				}
				imageRequest.N = common.GetPointer(uint(n))
			}
			imageRequest.Quality = formData.Get("quality")
			imageRequest.Size = formData.Get("size")
			if parameters := formData.Get("parameters"); parameters != "" {
				imageRequest.Extra = map[string]json.RawMessage{"parameters": json.RawMessage(parameters)}
			}
			if streamValue := strings.TrimSpace(formData.Get("stream")); streamValue != "" {
				stream, err := strconv.ParseBool(streamValue)
				if err != nil {
					return nil, fmt.Errorf("invalid stream value: %w", err)
				}
				imageRequest.Stream = common.GetPointer(stream)
			}
			if imageValue := formData.Get("image"); imageValue != "" {
				imageRequest.Image, _ = common.Marshal(imageValue)
			}

			if imageRequest.Model == "gpt-image-1" {
				if imageRequest.Quality == "" {
					imageRequest.Quality = "standard"
				}
			}
			if imageRequest.N == nil || *imageRequest.N == 0 {
				imageRequest.N = common.GetPointer(uint(1))
			}

			hasWatermark := formData.Has("watermark")
			if hasWatermark {
				watermark := formData.Get("watermark") == "true"
				imageRequest.Watermark = &watermark
			}
			break
		}
		fallthrough
	default:
		err := common.UnmarshalBodyReusable(c, imageRequest)
		if err != nil {
			return nil, err
		}

		if imageRequest.Model == "" {
			return nil, errors.New("model is required")
		}

		if strings.Contains(imageRequest.Size, "×") {
			return nil, errors.New("size an unexpected error occurred in the parameter, please use 'x' instead of the multiplication sign '×'")
		}

		if imageRequest.N != nil && *imageRequest.N > dto.MaxImageN {
			return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
		}

		if err := imageRequest.NormalizeLegacyDalleImageRequest(); err != nil {
			return nil, err
		}
		if imageRequest.Model == "gpt-image-1" {
			if imageRequest.Quality == "" {
				imageRequest.Quality = "auto"
			}
		}

		//if imageRequest.Prompt == "" {
		//	return nil, errors.New("prompt is required")
		//}

		if imageRequest.N == nil || *imageRequest.N == 0 {
			imageRequest.N = common.GetPointer(uint(1))
		}
	}

	// Provider parameters can override the top-level count. Validate before
	// pricing so malformed multipliers return a client error, not a pricing
	// failure after reservation has started.
	if raw, exists := imageRequest.Extra["parameters"]; exists {
		parameters := &dto.ImageBillingParameters{}
		if err := common.Unmarshal(raw, parameters); err != nil {
			return nil, fmt.Errorf("invalid image parameters: %w", err)
		}
		imageRequest.BillingParameters = parameters
	}
	if _, err := imageRequest.ImageCount(false); err != nil {
		return nil, err
	}
	return imageRequest, nil
}

// NovelAIRequestJSON returns the request JSON of a NovelAI generate-image
// call: the body itself, or for multipart/form-data the one field named
// request, sent as a form value or as a file part. The body is left in place
// for forwarding. Any other body must come without a Content-Type or as
// application/json, with unambiguous parameters and a UTF-8 charset if one is
// named.
//
// The upstream parses the same forwarded bytes with its own multipart parser,
// so a form that parsers can read differently is rejected instead of being
// billed as Go reads it. The form's Content-Type must name a boundary. Every
// part must carry exactly one form-data Content-Disposition with a non-empty
// name, and no transfer encoding but an identity one (Go's NextPart would
// decode quoted-printable unseen). These headers, and the request field's
// Content-Type, must be unambiguous (see unambiguousMediaParams).
func NovelAIRequestJSON(c *gin.Context) ([]byte, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	defer func() {
		if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
			c.Request.Body = io.NopCloser(storage)
		}
	}()
	contentType := c.Request.Header.Get("Content-Type")
	if base, _, _ := strings.Cut(contentType, ";"); !strings.EqualFold(strings.TrimSpace(base), gin.MIMEMultipartPOSTForm) {
		// The upstream decodes the body by its Content-Type as well; under
		// any other type or charset it could read other values than the ones
		// billed here.
		if contentType != "" {
			if mediaType, _, ok := unambiguousMediaParams(contentType); !ok || mediaType != gin.MIMEJSON {
				return nil, errors.New(i18n.T(c, i18n.MsgNovelAIContentTypeInvalid))
			}
		}
		return storage.Bytes()
	}
	invalid := func(reason string) error {
		return errors.New(i18n.T(c, i18n.MsgNovelAIMultipartInvalid, map[string]any{"Error": reason}))
	}
	_, params, ok := unambiguousMediaParams(contentType)
	if !ok || params["boundary"] == "" {
		return nil, invalid("ambiguous Content-Type")
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := multipart.NewReader(storage, params["boundary"])
	var requestJSON []byte
	found := false
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, invalid(err.Error())
		}
		for _, name := range []string{"Content-Disposition", "Content-Type", "Content-Transfer-Encoding"} {
			if len(part.Header.Values(name)) > 1 {
				return nil, invalid("repeated " + name)
			}
		}
		disposition, dispositionParams, ok := unambiguousMediaParams(part.Header.Get("Content-Disposition"))
		if !ok || disposition != "form-data" || dispositionParams["name"] == "" {
			return nil, invalid("ambiguous Content-Disposition")
		}
		switch strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))) {
		case "", "7bit", "8bit", "binary":
		default:
			return nil, invalid("unsupported Content-Transfer-Encoding")
		}
		if dispositionParams["name"] != "request" {
			continue
		}
		// The upstream reads one request; a second one could carry another
		// image count than the one billed.
		if found {
			return nil, errors.New(i18n.T(c, i18n.MsgNovelAIRequestFieldMissing))
		}
		if partType := part.Header.Get("Content-Type"); partType != "" {
			if _, _, ok := unambiguousMediaParams(partType); !ok {
				return nil, invalid("ambiguous Content-Type of the request field")
			}
		}
		if requestJSON, err = io.ReadAll(part); err != nil {
			return nil, invalid(err.Error())
		}
		found = true
	}
	if !found {
		return nil, errors.New(i18n.T(c, i18n.MsgNovelAIRequestFieldMissing))
	}
	return requestJSON, nil
}

// unambiguousMediaParams parses a Content-Type or Content-Disposition value
// of a request whose bytes another MIME parser reads too, and reports false
// unless every parser reads it alike: mime.ParseMediaType must accept it, no
// parameter may use RFC 2231 extended or continued syntax (name*=, name*0=),
// which ParseMediaType folds into the plain name while other parsers keep the
// plain value, and a charset must be UTF-8, since parsers that decode a field
// by it read other text than these bytes.
func unambiguousMediaParams(value string) (string, map[string]string, bool) {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "", nil, false
	}
	if charset, ok := params["charset"]; ok && !strings.EqualFold(charset, "utf-8") {
		return "", nil, false
	}
	// ParseMediaType accepted the value, so each parameter name follows an
	// unquoted ";" and ends at its "=".
	inName, quoted, escaped := false, false, false
	for _, r := range value {
		switch {
		case escaped:
			escaped = false
		case quoted:
			escaped = r == '\\'
			quoted = r != '"'
		case r == '"':
			quoted = true
		case r == ';':
			inName = true
		case r == '=':
			inName = false
		case r == '*' && inName:
			return "", nil, false
		}
	}
	return mediaType, params, true
}

// GetAndValidateNovelAIImageRequest validates a NovelAI generate-image request
// (JSON body or multipart request field) and returns it as an image request
// carrying only what billing reads: model, the input prompt and
// parameters.n_samples as the image count. The client body itself is
// forwarded unchanged.
func GetAndValidateNovelAIImageRequest(c *gin.Context) (*dto.ImageRequest, error) {
	requestJSON, err := NovelAIRequestJSON(c)
	if err != nil {
		return nil, err
	}
	if !gjson.ValidBytes(requestJSON) {
		return nil, errors.New(i18n.T(c, i18n.MsgNovelAIRequestInvalid))
	}
	request := gjson.ParseBytes(requestJSON)
	if !request.IsObject() {
		return nil, errors.New(i18n.T(c, i18n.MsgNovelAIRequestInvalid))
	}
	model, err := novelAIRequestMember(c, request, "model")
	if err != nil {
		return nil, err
	}
	if model.Type != gjson.String || model.String() == "" {
		return nil, errors.New(i18n.T(c, i18n.MsgDistributorModelNameRequired))
	}
	parameters, err := novelAIRequestMember(c, request, "parameters")
	if err != nil {
		return nil, err
	}
	// n_samples becomes the billing multiplier, so it is bounded here like the
	// OpenAI image n.
	count := 1
	if parameters.Exists() && parameters.Type != gjson.Null {
		if !parameters.IsObject() {
			return nil, errors.New(i18n.T(c, i18n.MsgNovelAIParametersInvalid))
		}
		samples, err := novelAIRequestMember(c, parameters, "n_samples")
		if err != nil {
			return nil, err
		}
		if samples.Exists() && samples.Type != gjson.Null {
			n, convErr := strconv.Atoi(samples.Raw)
			if samples.Type != gjson.Number || convErr != nil || n < 1 || n > dto.MaxImageN {
				return nil, errors.New(i18n.T(c, i18n.MsgNovelAINSamplesInvalid, map[string]any{"Max": dto.MaxImageN}))
			}
			count = n
		}
	}
	// The prompt feeds the sensitive word check and token estimation.
	input, err := novelAIRequestMember(c, request, "input")
	if err != nil {
		return nil, err
	}
	imageRequest := &dto.ImageRequest{Model: model.String(), N: common.GetPointer(uint(count))}
	if input.Type == gjson.String {
		imageRequest.Prompt = input.String()
	}
	return imageRequest, nil
}

// novelAIRequestMember returns the member key of a NovelAI request object.
// Keys match case-insensitively and must occur once, so a repeated or
// differently cased key cannot carry a value the upstream reads but billing
// does not.
func novelAIRequestMember(c *gin.Context, object gjson.Result, key string) (gjson.Result, error) {
	var member gjson.Result
	matches := 0
	object.ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), key) {
			member = value
			matches++
		}
		return true
	})
	if matches > 1 {
		return gjson.Result{}, errors.New(i18n.T(c, i18n.MsgNovelAIFieldRepeated, map[string]any{"Field": key}))
	}
	return member, nil
}

func GetAndValidateClaudeRequest(c *gin.Context) (textRequest *dto.ClaudeRequest, err error) {
	textRequest = &dto.ClaudeRequest{}
	err = common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}
	if textRequest.Messages == nil || len(textRequest.Messages) == 0 {
		return nil, errors.New("field messages is required")
	}
	if textRequest.Model == "" {
		return nil, errors.New("field model is required")
	}
	if ExceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxTokensToSample) {
		return nil, errors.New("max_tokens is invalid")
	}

	//if textRequest.Stream {
	//	relayInfo.IsStream = true
	//}

	return textRequest, nil
}

func GetAndValidateTextRequest(c *gin.Context, relayMode int) (*dto.GeneralOpenAIRequest, error) {
	textRequest := &dto.GeneralOpenAIRequest{}
	err := common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}

	if relayMode == relayconstant.RelayModeModerations && textRequest.Model == "" {
		textRequest.Model = "text-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && textRequest.Model == "" {
		textRequest.Model = c.Param("model")
	}

	if ExceedsMaxTokensLimit(textRequest.MaxTokens, textRequest.MaxCompletionTokens) {
		return nil, errors.New("max_tokens is invalid")
	}
	if ExceedsMaxTokensLimit(textRequest.MinTokens) {
		return nil, errors.New("min_tokens is invalid")
	}
	if textRequest.Model == "" {
		return nil, errors.New("model is required")
	}
	if textRequest.WebSearchOptions != nil {
		if textRequest.WebSearchOptions.SearchContextSize != "" {
			validSizes := map[string]bool{
				"high":   true,
				"medium": true,
				"low":    true,
			}
			if !validSizes[textRequest.WebSearchOptions.SearchContextSize] {
				return nil, errors.New("invalid search_context_size, must be one of: high, medium, low")
			}
		} else {
			textRequest.WebSearchOptions.SearchContextSize = "medium"
		}
	}
	switch relayMode {
	case relayconstant.RelayModeCompletions:
		if textRequest.Prompt == "" {
			return nil, errors.New("field prompt is required")
		}
	case relayconstant.RelayModeChatCompletions:
		// For FIM (Fill-in-the-middle) requests with prefix/suffix, messages is optional
		// It will be filled by provider-specific adaptors if needed (e.g., SiliconFlow)。Or it is allowed by model vendor(s) (e.g., DeepSeek)
		if len(textRequest.Messages) == 0 && textRequest.Prefix == nil && textRequest.Suffix == nil {
			return nil, errors.New("field messages is required")
		}
	case relayconstant.RelayModeEmbeddings:
	case relayconstant.RelayModeModerations:
		if textRequest.Input == nil || textRequest.Input == "" {
			return nil, errors.New("field input is required")
		}
	case relayconstant.RelayModeEdits:
		if textRequest.Instruction == "" {
			return nil, errors.New("field instruction is required")
		}
	}
	return textRequest, nil
}

func GetAndValidateGeminiRequest(c *gin.Context) (*dto.GeminiChatRequest, error) {
	request := &dto.GeminiChatRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if len(request.Contents) == 0 && len(request.Requests) == 0 {
		return nil, errors.New("contents is required")
	}
	if ExceedsMaxTokensLimit(request.GenerationConfig.MaxOutputTokens) {
		return nil, errors.New("maxOutputTokens is invalid")
	}

	//if c.Query("alt") == "sse" {
	//	relayInfo.IsStream = true
	//}

	return request, nil
}

func GetAndValidateGeminiEmbeddingRequest(c *gin.Context) (*dto.GeminiEmbeddingRequest, error) {
	request := &dto.GeminiEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}

func GetAndValidateGeminiBatchEmbeddingRequest(c *gin.Context) (*dto.GeminiBatchEmbeddingRequest, error) {
	request := &dto.GeminiBatchEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}
