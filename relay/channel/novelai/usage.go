package novelai

import (
	"strings"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
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
	// as. An HD image counts as the image of the canvas the request asked
	// for, not as the larger one it is delivered at: HD is the same image at
	// the same price, and a token-priced model would otherwise charge more
	// than twice as much for it.
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
//     which repeats the base caption on V4 and later; V3 models, which have
//     no V4 captions, read input only (promptTokens);
//   - negative text: the caption of parameters.v4_negative_prompt when it has
//     one, otherwise parameters.negative_prompt (uc, its older name, without
//     it), and on V3 negative_prompt (or uc) only;
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
// a list), the keys inside a caption included, so a key the upstream may read
// otherwise cannot lower the count.
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

	negative := stringValues(members(parameters, "negative_prompt"))
	if len(negative) == 0 {
		negative = stringValues(members(parameters, "uc"))
	}
	text := promptTokens(model, captionTexts(members(parameters, "v4_prompt")), stringValues(members(request, "input"))) +
		promptTokens(model, captionTexts(members(parameters, "v4_negative_prompt")), negative)

	width, height := relayconstant.NovelAIDefaultWidth, relayconstant.NovelAIDefaultHeight
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

// promptTokens is the estimate of one side of a request's prompt, positive or
// negative, from the texts of its V4 caption and of its plain prompt (input,
// or negative_prompt / uc) as model reads them. V3 has no V4 captions and
// reads the plain prompt. V4 and later read the caption when the request has
// one, and the plain prompt, which then repeats it, otherwise. A model the
// relay has no profile of may read either, so the larger counts. The two are
// never added: they are one prompt.
func promptTokens(model string, caption, plain []string) int {
	captionTokens, plainTokens := 0, 0
	for _, text := range caption {
		captionTokens += tokenkit.Estimate(model, text)
	}
	for _, text := range plain {
		plainTokens += tokenkit.Estimate(model, text)
	}
	modelProfile, known := profiles[model]
	switch {
	case !known:
		return max(captionTokens, plainTokens)
	case modelProfile.family == familyV3, len(caption) == 0:
		return plainTokens
	}
	return captionTokens
}

// captionTexts is the texts of the V4 caption objects among values
// ({caption:{base_caption, char_captions:[{char_caption}]}}) that have a
// string base_caption. A key repeated inside a caption counts all its values,
// as members reads them: JSON parsers differ in which one they keep.
func captionTexts(values []ojson.Result) []string {
	var texts []string
	for _, value := range values {
		for _, caption := range members(value, "caption") {
			bases := stringValues(members(caption, "base_caption"))
			if len(bases) == 0 {
				continue
			}
			texts = append(texts, bases...)
			for _, characters := range members(caption, "char_captions") {
				characters.ForEach(func(_, character ojson.Result) bool {
					texts = append(texts, stringValues(members(character, "char_caption"))...)
					return true
				})
			}
		}
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
