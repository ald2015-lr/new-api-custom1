package novelai

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/tokenkit"

	"github.com/MoeForge/moego/ojson"
)

// NovelAI reports no usage, so the relay counts a generation itself: its
// prompts by the token estimate and its images by their area.
const (
	// imageTokenArea is the area service.InlineImageCompletionTokens
	// describes: 1290 tokens is Gemini's documented size of one generated
	// image up to 1024x1024, which this fork already counts for an image in
	// a chat answer.
	imageTokenArea = 1024 * 1024
	// defaultWidth and defaultHeight are NovelAI's canvas when a request
	// names none.
	defaultWidth  = 832
	defaultHeight = 1216
	// maxReferenceImages bounds the images counted of one reference list:
	// NovelAI takes at most 16 vibes, and a longer list fails there.
	maxReferenceImages = 16
)

// ImageTokens is what one image of a width x height canvas counts as, made by
// NovelAI or given to it: service.InlineImageCompletionTokens for 1024x1024,
// scaled by the area and rounded up, so a 64x64 image is not worth a
// 1024x1024 one and a 3 MP one is worth three; NovelAI's own cost grows with
// the area too. The area is capped at NovelAI's limit (maxPixels), so a
// request cannot claim more, and a side under 1 counts as 1.
func ImageTokens(width, height int) int {
	width, height = min(max(width, 1), maxPixels), min(max(height, 1), maxPixels)
	area := min(int64(width)*int64(height), maxPixels)
	return int((int64(service.InlineImageCompletionTokens)*area + imageTokenArea - 1) / imageTokenArea)
}

// GenerationTokens is what a NovelAI generation counts as in tokens.
type GenerationTokens struct {
	// Text is the estimate of the positive and negative prompts NovelAI
	// receives, presets included, at least 1 so that a per-image price still
	// settles: a usage without tokens bills nothing.
	Text int
	// InputImages is ImageTokens of the canvas for each image NovelAI
	// conditions on: the source of an img2img or infill (not its mask), and
	// each vibe transfer or Precise Reference image.
	InputImages int
	// PerImage is ImageTokens of the canvas: what each generated image counts
	// as.
	PerImage int
}

// Usage is the usage of the generation when it delivered images images:
// prompt tokens are the text and the input images, completion tokens the
// generated images. The split goes into prompt_tokens_details, where a
// ratio price bills the image tokens at the model's image ratio (1 unless
// set, the price of the other prompt tokens) and an expression at img when
// it prices img; a per-call price and an image_count expression do not read
// tokens. images is the quantity the request is billed at
// (RelayInfo.SettledImageCount), so the log's tokens and image count agree.
func (t GenerationTokens) Usage(images int) *dto.Usage {
	prompt := t.Text + t.InputImages
	completion := max(images, 0) * t.PerImage
	return &dto.Usage{
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		TotalTokens:         prompt + completion,
		PromptTokensDetails: dto.InputTokenDetails{TextTokens: t.Text, ImageTokens: t.InputImages},
	}
}

// RequestTokens counts a NovelAI generate-image request, as JSON (the body a
// client sent, or the request field of its multipart form), as NovelAI reads
// it. BuildGeneration counts its own requests with it too.
//   - positive text: the caption of parameters.v4_prompt (base_caption and
//     each char_captions[].char_caption) when it has one, otherwise input,
//     which repeats the base caption on V4 and later;
//   - negative text: the caption of parameters.v4_negative_prompt when it has
//     one, otherwise parameters.negative_prompt (uc, its older name, without
//     it);
//   - canvas: parameters.width x parameters.height, NovelAI's 832x1216 for a
//     side the request does not give as a positive whole number;
//   - input images: the source of an img2img or infill action, and each
//     entry of reference_image_multiple and director_reference_images, as
//     base64 in JSON or as the *_cached lists a multipart form sends instead,
//     at most maxReferenceImages a list. The image parts of a multipart form
//     are named by its JSON, so nothing else of the form is read.
//
// Keys match case-insensitively, as the request validator matches the keys it
// reads, and a repeated key counts all its values (the largest of a number or
// a list), so a key the upstream may read otherwise cannot lower the count.
func RequestTokens(requestJSON []byte) GenerationTokens {
	request := ojson.ParseBytes(requestJSON)
	var model string
	if models := stringValues(members(request, "model")); len(models) > 0 {
		model = models[0]
	}
	var parameters ojson.Result
	for _, value := range members(request, "parameters") {
		if value.IsObject() {
			parameters = value
			break
		}
	}

	positive := captionTexts(members(parameters, "v4_prompt"))
	if len(positive) == 0 {
		positive = stringValues(members(request, "input"))
	}
	negative := captionTexts(members(parameters, "v4_negative_prompt"))
	if len(negative) == 0 {
		negative = stringValues(members(parameters, "negative_prompt"))
	}
	if len(negative) == 0 {
		negative = stringValues(members(parameters, "uc"))
	}
	text := 0
	for _, prompt := range append(positive, negative...) {
		text += tokenkit.Estimate(model, prompt)
	}

	width, height := defaultWidth, defaultHeight
	if side := largestSide(members(parameters, "width")); side > 0 {
		width = side
	}
	if side := largestSide(members(parameters, "height")); side > 0 {
		height = side
	}
	perImage := ImageTokens(width, height)

	inputImages := 0
	for _, action := range stringValues(members(request, "action")) {
		if action == ActionImg2Img || action == ActionInfill {
			inputImages = 1
		}
	}
	for _, list := range []string{"reference_image_multiple", "director_reference_images"} {
		entries := max(listEntries(members(parameters, list), ojson.String), listEntries(members(parameters, list+"_cached"), ojson.JSON))
		inputImages += min(entries, maxReferenceImages)
	}
	return GenerationTokens{Text: max(text, 1), InputImages: inputImages * perImage, PerImage: perImage}
}

// members is every value of key in a JSON object, the key matched
// case-insensitively.
func members(object ojson.Result, key string) []ojson.Result {
	if !object.IsObject() {
		return nil
	}
	var values []ojson.Result
	object.ForEach(func(name, value ojson.Result) bool {
		if strings.EqualFold(name.String(), key) {
			values = append(values, value)
		}
		return true
	})
	return values
}

// stringValues is the strings among values.
func stringValues(values []ojson.Result) []string {
	var texts []string
	for _, value := range values {
		if value.Type == ojson.String {
			texts = append(texts, value.Str)
		}
	}
	return texts
}

// captionTexts is the texts of the V4 caption objects among values
// ({caption:{base_caption, char_captions:[{char_caption}]}}) that have a
// string base_caption.
func captionTexts(values []ojson.Result) []string {
	var texts []string
	for _, value := range values {
		base := value.Get("caption.base_caption")
		if !value.IsObject() || base.Type != ojson.String {
			continue
		}
		texts = append(texts, base.Str)
		value.Get("caption.char_captions").ForEach(func(_, character ojson.Result) bool {
			if caption := character.Get("char_caption"); caption.Type == ojson.String {
				texts = append(texts, caption.Str)
			}
			return true
		})
	}
	return texts
}

// largestSide is the largest positive whole number among values, 0 when
// there is none.
func largestSide(values []ojson.Result) int {
	side := 0
	for _, value := range values {
		if value.Type == ojson.Number && value.Num >= 1 && value.Num == float64(int64(value.Num)) {
			side = max(side, int(min(value.Num, maxPixels)))
		}
	}
	return side
}

// listEntries is the most entries of the given kind a list among values
// has: non-empty strings (base64 images) for ojson.String, objects (the
// {cache_secret_key, data} entries of a *_cached list) for ojson.JSON.
func listEntries(values []ojson.Result, kind ojson.Type) int {
	most := 0
	for _, value := range values {
		if !value.IsArray() {
			continue
		}
		count := 0
		value.ForEach(func(_, entry ojson.Result) bool {
			if kind == ojson.String && entry.Type == ojson.String && entry.Str != "" || kind == ojson.JSON && entry.IsObject() {
				count++
			}
			return true
		})
		most = max(most, count)
	}
	return most
}
