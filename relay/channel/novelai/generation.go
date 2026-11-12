package novelai

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"

	"github.com/gin-gonic/gin"
)

// NovelAI's generation actions.
const (
	ActionGenerate = "generate"
	ActionImg2Img  = "img2img"
	ActionInfill   = "infill"
)

const (
	// maxPixels is NovelAI's canvas limit, width x height.
	maxPixels = 3_145_728
	// autoEditPixels is the area of an edit whose size is auto: the
	// source's aspect ratio at at most 1024x1024, Opus's free area.
	autoEditPixels = 1_048_576
	// defaultSide is the side of a square generation whose size is auto.
	defaultSide = 1024
	// maxSizeSide bounds a side parsed from an OpenAI size.
	maxSizeSide = 65_536
	// MaxSeed is the largest seed NovelAI takes; it reads the seed unsigned,
	// and 0 draws a random image while its metadata says 0.
	MaxSeed = 4_294_967_288

	defaultSteps     = 23
	mediumSteps      = 14
	defaultSampler   = "k_euler_ancestral"
	defaultSchedule  = "karras"
	img2imgStrength  = 0.7
	transparentTag   = "transparent background"
	varietyArea      = 832 * 1216
	nsfwTag          = "nsfw"
	infillingRequest = "NativeInfillingRequest"
)

// GenerationIntent is an OpenAI-format request (images or chat) reduced to
// what NovelAI generates. The action follows from the inputs: a Mask makes
// it "infill" (with the model's inpainting model), an Image alone "img2img",
// neither "generate".
type GenerationIntent struct {
	// Model is the NovelAI model the channel maps the request to.
	Model  string
	Prompt string
	// Size is the OpenAI size: "", "auto" or "WIDTHxHEIGHT".
	Size string
	// N is the image count billing reserved for the request (1 to
	// dto.MaxImageN). It becomes n_samples and is never lowered here.
	N int
	// Image is the source image (PNG, JPEG or WebP bytes) of an edit.
	Image []byte
	// Mask marks the area to edit: OpenAI's convention (fully transparent
	// pixels), or a black-and-white NovelAI mask (white) when it has no
	// transparent pixel.
	Mask []byte
	// ImageFormat is the format to ask NovelAI for, "png" (the default) or
	// "webp".
	ImageFormat string
	// Transparent asks for a transparent background (V5 only).
	Transparent bool
	// PresetsOff makes the quality tags and the UC preset opt-in (the
	// channel's novelai_presets_disabled setting); they are on by default,
	// as in NovelAI's web client.
	PresetsOff bool
	// Extras are the top-level NovelAI extension keys of the request, as
	// ParseExtensions returns them: negative_prompt, seed, steps, scale,
	// strength, noise, characterPrompts and the rest of extensionKeys.
	Extras map[string]json.RawMessage
	// Parameters is the request's "parameters" object in NovelAI wire names.
	// Its keys override Extras; keys the relay does not interpret are
	// forwarded as sent.
	Parameters map[string]json.RawMessage
}

// Generation is a NovelAI request built from a GenerationIntent, with what
// the response needs to know about it.
type Generation struct {
	// Body is the JSON request for /ai/generate-image; RequestBody adds the
	// stream mode for /ai/generate-image-stream.
	Body   map[string]any
	Model  string
	Action string
	Width  int
	Height int
	// NSamples is the number of images NovelAI is asked for.
	NSamples int
	// Steps is the number of sampling steps, which a stream previews.
	Steps int
	// Seed is the seed of the first image; NovelAI uses Seed+i for image i.
	Seed int64
	// Prompt is the final positive prompt (input), presets included.
	Prompt string
	// ImageFormat is the image_format asked for.
	ImageFormat string
	Transparent bool
	// Streamable tells whether the model serves /ai/generate-image-stream;
	// V3 models return only final images.
	Streamable bool
}

// Size is the generated size in OpenAI's WIDTHxHEIGHT form.
func (g *Generation) Size() string {
	return strconv.Itoa(g.Width) + "x" + strconv.Itoa(g.Height)
}

// RequestBody returns the request JSON. With stream it carries
// parameters.stream "sse" for /ai/generate-image-stream; /ai/generate-image
// ignores the field, so it is left out there.
func (g *Generation) RequestBody(stream bool) map[string]any {
	if !stream {
		return g.Body
	}
	body := maps.Clone(g.Body)
	parameters := maps.Clone(g.Body["parameters"].(map[string]any))
	parameters["stream"] = "sse"
	body["parameters"] = parameters
	return body
}

// Usage is what a converted request reports and bills as tokens: the
// estimate of the final prompt (at least 1) as prompt tokens and nothing as
// completion, as NovelAI reports no usage and image bytes are not text.
func (g *Generation) Usage() *dto.Usage {
	prompt := max(tokenkit.Estimate(g.Model, g.Prompt), 1)
	return &dto.Usage{PromptTokens: prompt, TotalTokens: prompt}
}

// relayOwnedParameters are the keys of parameters the relay sets itself. A
// request that sets one is rejected rather than silently overridden.
var relayOwnedParameters = map[string]bool{
	"model": true, "action": true, "image": true, "mask": true, "stream": true,
	"use_new_shared_trial": true, "recaptcha_token": true, "url": true,
}

// BuildGeneration turns an intent into the NovelAI request. Precedence,
// highest first: the relay's own keys (model, action, image, mask, stream,
// n_samples = N, ...), the parameters object, the top-level extension keys,
// the OpenAI fields (size, n, output format, background), the model's
// defaults. The model's rules apply last: V5 always uses karras and has no
// SMEA, Variety+ or decrisper, V5 Full Medium fixes its steps, sampler and UC,
// V3 has no V4 captions or characters. Presets are expanded as the NovelAI
// web client does (the server does not): the quality tags go after the
// prompt (withPresetTags) and the UC preset in front of the negative prompt,
// unless the request turns them off. A failure is a 400 that skips retries:
// another channel would reject the request the same way. An edit's image
// and mask are decoded last, once the request holds the memory they need of
// imageWork; a client that leaves while it waits ends the build.
func BuildGeneration(c *gin.Context, intent GenerationIntent) (*Generation, *types.NewAPIError) {
	model := intent.Model
	modelProfile, ok := profiles[model]
	if !ok {
		return nil, badRequest(c, i18n.MsgNovelAIModelUnsupported, map[string]any{"Model": model})
	}
	action := ActionGenerate
	switch {
	case intent.Mask != nil:
		if intent.Image == nil {
			return nil, badRequest(c, i18n.MsgNovelAIImageMissing, nil)
		}
		action = ActionInfill
		if !inpaintingModels[model] {
			model = modelProfile.inpainting
			modelProfile = profiles[model]
		}
	case intent.Image != nil:
		action = ActionImg2Img
	}
	if inpaintingModels[model] && action != ActionInfill {
		return nil, badRequest(c, i18n.MsgNovelAIInpaintingNeedsMask, map[string]any{"Model": model})
	}

	user := maps.Clone(intent.Extras)
	if user == nil {
		user = map[string]json.RawMessage{}
	}
	for key, value := range intent.Parameters {
		if relayOwnedParameters[key] || strings.HasSuffix(key, "_cache_secret_key") || strings.HasSuffix(key, "_cached") {
			return nil, badRequest(c, i18n.MsgNovelAIFieldRelayOwned, map[string]any{"Field": key})
		}
		if key == "n" {
			// new-api's billing parameter, not NovelAI's: the count is n.
			continue
		}
		user[key] = value
	}
	fields := requestFields{c: c, values: user}

	// The headers of the image and the mask are checked first; they are
	// decoded once everything else is valid.
	var source image.Rectangle
	var sourceHeader, maskHeader imageHeader
	if intent.Image != nil {
		header, err := imageInputHeader(c, intent.Image, "image")
		if err != nil {
			return nil, err
		}
		sourceHeader, source = header, image.Rect(0, 0, header.Width, header.Height)
	}
	if intent.Mask != nil {
		header, err := imageInputHeader(c, intent.Mask, "mask")
		if err != nil {
			return nil, err
		}
		maskHeader = header
	}
	width, height, apiErr := generationSize(c, fields, intent.Size, source)
	if apiErr != nil {
		return nil, apiErr
	}

	nSamples := max(intent.N, 1)
	if requested, err := fields.integer("n_samples", 1, dto.MaxImageN); err != nil {
		return nil, err
	} else if requested != nil && int(*requested) != nSamples {
		return nil, badRequest(c, i18n.MsgNovelAINSamplesMismatch, map[string]any{"N": nSamples})
	}
	if limit := sampleLimit(width * height); nSamples > limit {
		return nil, badRequest(c, i18n.MsgNovelAINTooLarge, map[string]any{"Max": limit, "Width": width, "Height": height})
	}

	seed, apiErr := generationSeed(c, fields)
	if apiErr != nil {
		return nil, apiErr
	}

	parameters := map[string]any{
		"params_version":                        4,
		"width":                                 width,
		"height":                                height,
		"n_samples":                             nSamples,
		"seed":                                  seed,
		"autoSmea":                              false,
		"controlnet_strength":                   1,
		"legacy":                                false,
		"legacy_v3_extend":                      false,
		"add_original_image":                    true,
		"normalize_reference_strength_multiple": true,
		"deliberate_euler_ancestral_bug":        false,
		"prefer_brownian":                       true,
	}
	if version, err := fields.integer("params_version", 1, 4); err != nil {
		return nil, err
	} else if version != nil {
		parameters["params_version"] = *version
	}

	steps := int64(defaultSteps)
	if requested, err := fields.integer("steps", 1, 50); err != nil {
		return nil, err
	} else if requested != nil {
		steps = *requested
	}
	scale := modelProfile.scale
	if requested, err := fields.number("scale", 0, 10); err != nil {
		return nil, err
	} else if requested != nil {
		scale = *requested
	}
	cfgRescale := 0.0
	if requested, err := fields.number("cfg_rescale", 0, 1); err != nil {
		return nil, err
	} else if requested != nil {
		cfgRescale = *requested
	}
	if modelProfile.medium {
		steps = mediumSteps
	} else {
		parameters["cfg_rescale"] = cfgRescale
	}
	parameters["steps"] = steps
	parameters["scale"] = scale

	sampler, smea, smeaDyn, apiErr := generationSampler(c, fields, modelProfile, model, action)
	if apiErr != nil {
		return nil, apiErr
	}
	if sampler == "ddim_v3" && slices.Contains(ddimFailingSteps, steps) {
		// NovelAI's DDIM fails at these step counts; novelai-bridge falls
		// back to k_euler_ancestral, with a noise schedule.
		sampler = defaultSampler
	}
	parameters["sampler"] = sampler
	schedule := defaultSchedule
	if requested, err := fields.choice("noise_schedule", noiseSchedules, model); err != nil {
		return nil, err
	} else if requested != "" {
		schedule = requested
	}
	if modelProfile.family == familyV5 || modelProfile.family != familyV3 && schedule == "native" {
		schedule = defaultSchedule
	}
	if sampler != "ddim_v3" {
		parameters["noise_schedule"] = schedule
	}

	switch modelProfile.family {
	case familyV3:
		parameters["sm"] = smea
		parameters["sm_dyn"] = smeaDyn
		decrisp, err := fields.boolean("dynamic_thresholding")
		if err != nil {
			return nil, err
		}
		parameters["dynamic_thresholding"] = decrisp != nil && *decrisp
	default:
		// Decrisper is V3's; the web client still sends it off.
		parameters["dynamic_thresholding"] = false
	}
	if modelProfile.family != familyV5 {
		sigma, err := fields.varietySigma(modelProfile, width, height)
		if err != nil {
			return nil, err
		}
		parameters["skip_cfg_above_sigma"] = sigma
	}

	imageFormat := intent.ImageFormat
	if imageFormat == "" {
		imageFormat = "png"
	}
	if requested, err := fields.choice("image_format", []string{"png", "webp"}, model); err != nil {
		return nil, err
	} else if requested != "" {
		imageFormat = requested
	}
	parameters["image_format"] = imageFormat
	if intent.Transparent && modelProfile.family != familyV5 {
		return nil, badRequest(c, i18n.MsgNovelAITransparentUnsupported, nil)
	}

	quality, uc, apiErr := generationPresets(c, fields, modelProfile, model, intent.PresetsOff)
	if apiErr != nil {
		return nil, apiErr
	}
	parameters["qualityPresetId"] = quality
	parameters["ucPresetId"] = uc
	parameters["tag_hint_qt"] = tagHintQuality[quality]
	parameters["tag_hint_uc_preset"] = tagHintUC[uc]

	// The prompts: the request's V4 captions verbatim when it sends them,
	// otherwise the prompt with the transparency tag and the quality tags,
	// and the negative prompt behind the UC preset.
	explicitPrompt, apiErr := fields.caption("v4_prompt", modelProfile)
	if apiErr != nil {
		return nil, apiErr
	}
	explicitNegative, apiErr := fields.caption("v4_negative_prompt", modelProfile)
	if apiErr != nil {
		return nil, apiErr
	}
	prompt := intent.Prompt
	if explicitPrompt != nil {
		prompt = explicitPrompt.baseCaption
	} else {
		tags := modelProfile.quality[quality]
		if intent.Transparent {
			tags = joinTags(transparentTag, tags)
		}
		prompt = withPresetTags(prompt, tags, modelProfile.family == familyV3)
	}
	userNegative, apiErr := fields.str("negative_prompt")
	if apiErr != nil {
		return nil, apiErr
	}
	if modelProfile.medium {
		// V5 Full Medium takes no custom UC: its negative is the preset.
		userNegative = ""
	}
	negative := userNegative
	if explicitNegative != nil {
		negative = explicitNegative.baseCaption
	} else {
		prefix := modelProfile.uc[uc]
		if uc == presetNone && userNegative != "" {
			prefix = ""
		}
		negative = joinTags(prefix, userNegative)
		if !modelProfile.curated && uc != presetNone && prefix != "" && !strings.Contains(strings.ToLower(prompt), nsfwTag) {
			negative = nsfwTag + ", " + negative
		}
	}
	parameters["negative_prompt"] = negative

	characters, apiErr := fields.characters(modelProfile, model)
	if apiErr != nil {
		return nil, apiErr
	}
	useCoords := false
	characterPrompts := make([]map[string]any, 0, len(characters))
	charCaptions := make([]map[string]any, 0, len(characters))
	charNegatives := make([]map[string]any, 0, len(characters))
	for _, character := range characters {
		useCoords = useCoords || character.positioned
		center := map[string]any{"x": character.x, "y": character.y}
		characterUC := character.uc
		if modelProfile.medium {
			characterUC = ""
		}
		characterPrompts = append(characterPrompts, map[string]any{"prompt": character.prompt, "uc": characterUC, "center": center, "enabled": true})
		charCaptions = append(charCaptions, map[string]any{"char_caption": character.prompt, "centers": []any{center}})
		charNegatives = append(charNegatives, map[string]any{"char_caption": characterUC, "centers": []any{center}})
	}
	parameters["characterPrompts"] = characterPrompts
	if modelProfile.family != familyV3 {
		legacyUC, err := fields.boolean("legacy_uc")
		if err != nil {
			return nil, err
		}
		parameters["use_coords"] = useCoords
		parameters["legacy_uc"] = legacyUC != nil && *legacyUC
		parameters["inpaintImg2ImgStrength"] = 1
		if explicitPrompt != nil {
			parameters["v4_prompt"] = explicitPrompt.raw
		} else {
			parameters["v4_prompt"] = map[string]any{
				"caption":    map[string]any{"base_caption": prompt, "char_captions": charCaptions},
				"use_coords": useCoords,
				"use_order":  true,
			}
		}
		if explicitNegative != nil {
			parameters["v4_negative_prompt"] = explicitNegative.raw
		} else {
			parameters["v4_negative_prompt"] = map[string]any{
				"caption":   map[string]any{"base_caption": negative, "char_captions": charNegatives},
				"legacy_uc": legacyUC != nil && *legacyUC,
			}
		}
	}
	if modelProfile.family == familyV5 {
		parameters["straight_alpha"] = true
		if intent.Transparent {
			parameters["tag_hint_transparent_background"] = true
		}
	}

	if action != ActionGenerate {
		parameters["extra_noise_seed"] = seed
		noise := 0.0
		if requested, err := fields.number("noise", 0, 0.99); err != nil {
			return nil, err
		} else if requested != nil {
			noise = *requested
		}
		if action == ActionImg2Img {
			strength := img2imgStrength
			if requested, err := fields.number("strength", 0.01, 0.99); err != nil {
				return nil, err
			} else if requested != nil {
				strength = *requested
			}
			parameters["strength"] = strength
			parameters["noise"] = noise
			parameters["color_correct"] = true
		} else {
			strength := 1.0
			if requested, err := fields.number("strength", 0.01, 1); err != nil {
				return nil, err
			} else if requested != nil {
				strength = *requested
			}
			if strength < 1 {
				if modelProfile.family == familyV3 {
					// V3 inpaints without img2img conditioning.
					return nil, badRequest(c, i18n.MsgNovelAIFieldValue, map[string]any{"Field": "strength", "Value": strconv.FormatFloat(strength, 'f', -1, 64), "Model": model})
				}
				// Both wire forms: the nested group the web client sends
				// since 2026-10 and the flat strength older clients send.
				parameters["img2img"] = map[string]any{"strength": strength, "noise": noise, "color_correct": true, "extra_noise_seed": seed}
				parameters["inpaintImg2ImgStrength"] = strength
			}
			parameters["request_type"] = infillingRequest
		}

		// The image work takes its memory of imageWork, waiting while
		// other requests' work holds it: the decoded image and mask, the
		// resize's columns (coverResize), and three RGBA images of the
		// canvas (the image, the mask, and the PNGs encoded from them).
		work := min(imageWorkBudget, sourceHeader.bytes+maskHeader.bytes+4*int64(width)*int64(coverCrop(source, width, height).Dy()+3*height))
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		if err := imageWork.Acquire(ctx, work); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
		}
		defer imageWork.Release(work)
		if action == ActionInfill {
			mask, err := decodeImageInput(c, intent.Mask, maskHeader, "mask")
			if err != nil {
				return nil, err
			}
			encodedMask, ok := novelAIMask(mask, source, width, height)
			if !ok {
				return nil, badRequest(c, i18n.MsgNovelAIMaskEmpty, nil)
			}
			parameters["mask"] = encodedMask
		}
		decoded, err := decodeImageInput(c, intent.Image, sourceHeader, "image")
		if err != nil {
			return nil, err
		}
		parameters["image"] = encodePNGBase64(coverResize(decoded, coverCrop(decoded.Bounds(), width, height), width, height))
	}

	// The rest of parameters goes to NovelAI as sent.
	for key, value := range intent.Parameters {
		if !interpretedKeys[key] && key != "n" {
			parameters[key] = value
		}
	}

	return &Generation{
		Body: map[string]any{
			"input":                prompt,
			"model":                model,
			"action":               action,
			"parameters":           parameters,
			"use_new_shared_trial": true,
		},
		Model:       model,
		Action:      action,
		Width:       width,
		Height:      height,
		NSamples:    nSamples,
		Steps:       int(steps),
		Seed:        seed,
		Prompt:      prompt,
		ImageFormat: imageFormat,
		Transparent: intent.Transparent,
		Streamable:  modelProfile.family != familyV3,
	}, nil
}

// ddimFailingSteps are the step counts at which V3's DDIM fails on NovelAI.
var ddimFailingSteps = []int64{3, 9, 27, 36, 37}

// interpretedKeys are the parameters the builder reads and normalizes; the
// value it settles on replaces the request's. Any other key of parameters is
// forwarded as sent, overriding the relay's default for it.
var interpretedKeys = map[string]bool{
	"negative_prompt": true, "seed": true, "steps": true, "scale": true, "cfg_rescale": true,
	"sampler": true, "noise_schedule": true, "sm": true, "sm_dyn": true, "dynamic_thresholding": true,
	"ucPreset": true, "ucPresetId": true, "qualityToggle": true, "qualityPresetId": true,
	"strength": true, "noise": true, "characterPrompts": true, "skip_cfg_above_sigma": true,
	"variety": true, "image_format": true, "params_version": true, "legacy_uc": true,
	"width": true, "height": true, "n_samples": true, "v4_prompt": true, "v4_negative_prompt": true,
}

// sampleLimit is the most images NovelAI generates at once for an area.
func sampleLimit(pixels int) int {
	switch {
	case pixels <= 360_448:
		return 8
	case pixels <= 409_600:
		return 6
	default:
		return 4
	}
}

// generationSize resolves the canvas: parameters.width / height when the
// request sets them (exact multiples of 64 within NovelAI's area), otherwise
// the OpenAI size rounded to multiples of 64 and scaled into NovelAI's area.
// An auto size is 1024x1024 for a generation (or the canvas of the extension
// aspect_ratio, aspectSize) and the aspect ratio of the source (its bounds,
// empty without one) within 1024x1024's area for an edit.
func generationSize(c *gin.Context, fields requestFields, size string, source image.Rectangle) (int, int, *types.NewAPIError) {
	invalidDimensions := badRequest(c, i18n.MsgNovelAIDimensionsInvalid, map[string]any{"Max": maxPixels})
	var width, height int
	size = strings.TrimSpace(size)
	switch {
	case size == "" || strings.EqualFold(size, "auto"):
		aspectWidth, aspectHeight, err := fields.aspectSize()
		if err != nil {
			return 0, 0, err
		}
		switch {
		case aspectWidth > 0:
			width, height = aspectWidth, aspectHeight
		case !source.Empty():
			width, height = areaSize(float64(source.Dx())/float64(source.Dy()), autoEditPixels)
		default:
			width, height = defaultSide, defaultSide
		}
	default:
		w, h, ok := parseSize(size)
		if !ok {
			return 0, 0, badRequest(c, i18n.MsgNovelAISizeInvalid, map[string]any{"Max": maxSizeSide})
		}
		width, height = fitSize(w, h)
	}
	for key, side := range map[string]*int{"width": &width, "height": &height} {
		requested, err := fields.integer(key, 64, maxPixels/64)
		if err != nil {
			return 0, 0, invalidDimensions
		}
		if requested != nil {
			if *requested%64 != 0 {
				return 0, 0, invalidDimensions
			}
			*side = int(*requested)
		}
	}
	if width*height > maxPixels {
		return 0, 0, invalidDimensions
	}
	return width, height, nil
}

// parseSize reads WIDTHxHEIGHT.
func parseSize(size string) (int, int, bool) {
	w, h, ok := strings.Cut(strings.ToLower(size), "x")
	if !ok {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(w))
	height, errH := strconv.Atoi(strings.TrimSpace(h))
	if errW != nil || errH != nil || width < 1 || height < 1 || width > maxSizeSide || height > maxSizeSide {
		return 0, 0, false
	}
	return width, height, true
}

// fitSize rounds each side to the nearest multiple of 64 (at least 64) and,
// past NovelAI's area, scales both down into it, rounding down to 64.
func fitSize(width, height int) (int, int) {
	round := func(side float64) int { return max(64, int(math.Round(side/64))*64) }
	w, h := round(float64(width)), round(float64(height))
	if w*h <= maxPixels {
		return w, h
	}
	factor := math.Sqrt(float64(maxPixels) / float64(w*h))
	return withinArea(floor64(float64(w)*factor), floor64(float64(h)*factor), maxPixels)
}

// areaSize is the canvas of an aspect ratio at at most pixels, both sides
// rounded down to 64.
func areaSize(aspect float64, pixels int) (int, int) {
	return withinArea(floor64(math.Sqrt(float64(pixels)*aspect)), floor64(math.Sqrt(float64(pixels)/aspect)), pixels)
}

// floor64 rounds a side down to a multiple of 64, at least 64.
func floor64(side float64) int {
	return max(64, int(side/64)*64)
}

// withinArea brings a canvas back to at most pixels when a side held at 64
// left the other one too long.
func withinArea(width, height, pixels int) (int, int) {
	switch {
	case width*height <= pixels:
		return width, height
	case width > height:
		return floor64(float64(pixels / height)), height
	}
	return width, floor64(float64(pixels / width))
}

// aspectPresets are the canvases nai-shengtu gives the common aspect ratios;
// other ratios get areaSize at about 1024x1024.
var aspectPresets = map[string][2]int{
	"1:1": {1024, 1024}, "2:3": {832, 1216}, "3:2": {1216, 832}, "3:4": {896, 1152}, "4:3": {1152, 896},
	"4:5": {896, 1088}, "5:4": {1088, 896}, "9:16": {768, 1344}, "16:9": {1344, 768}, "9:21": {640, 1536}, "21:9": {1536, 640},
}

// generationSeed is the request's seed, or a random one when it sends none,
// 0 or -1, so the response can report the seed of every image.
func generationSeed(c *gin.Context, fields requestFields) (int64, *types.NewAPIError) {
	raw, ok := fields.values["seed"]
	if ok && !isJSONNull(raw) {
		seed, isInteger := jsonInteger(raw)
		if !isInteger {
			return 0, badRequest(c, i18n.MsgNovelAIFieldRange, map[string]any{"Field": "seed", "Min": 1, "Max": MaxSeed})
		}
		if seed != 0 && seed != -1 {
			if seed < 1 || seed > MaxSeed {
				return 0, badRequest(c, i18n.MsgNovelAIFieldRange, map[string]any{"Field": "seed", "Min": 1, "Max": MaxSeed})
			}
			return seed, nil
		}
	}
	return rand.Int64N(MaxSeed) + 1, nil
}

// generationSampler resolves the sampler against the family's list: ddim is
// ddim_v3 on V3 and k_euler_ancestral elsewhere and on inpainting models, and
// V3's nai_smea / nai_smea_dyn are k_euler_ancestral with SMEA (and DYN).
// SMEA exists on V3 only (it fails V4.5 and V5 with a 500) and not with an
// image input or DDIM. V5 Full Medium always uses k_euler_ancestral.
func generationSampler(c *gin.Context, fields requestFields, modelProfile *profile, model, action string) (string, bool, bool, *types.NewAPIError) {
	sampler, apiErr := fields.str("sampler")
	if apiErr != nil {
		return "", false, false, apiErr
	}
	smea, apiErr := fields.boolean("sm")
	if apiErr != nil {
		return "", false, false, apiErr
	}
	smeaDyn, apiErr := fields.boolean("sm_dyn")
	if apiErr != nil {
		return "", false, false, apiErr
	}
	sm, smDyn := smea != nil && *smea, smeaDyn != nil && *smeaDyn
	unsupported := func(field, value string) *types.NewAPIError {
		return badRequest(c, i18n.MsgNovelAIFieldValue, map[string]any{"Field": field, "Value": value, "Model": model})
	}
	if sampler == "" {
		sampler = defaultSampler
	}
	allowed := samplersV4
	if modelProfile.family == familyV3 {
		allowed = samplersV3
		switch sampler {
		case "ddim":
			sampler = "ddim_v3"
		case "nai_smea":
			sampler, sm = defaultSampler, true
		case "nai_smea_dyn":
			sampler, sm, smDyn = defaultSampler, true, true
		}
	} else {
		if sm || smDyn {
			field := "sm"
			if !sm {
				field = "sm_dyn"
			}
			return "", false, false, unsupported(field, "true")
		}
		if sampler == "ddim" {
			sampler = defaultSampler
		}
	}
	if !slices.Contains(allowed, sampler) {
		return "", false, false, unsupported("sampler", sampler)
	}
	if action == ActionInfill && sampler == "ddim_v3" {
		sampler = defaultSampler
	}
	if modelProfile.medium {
		sampler = defaultSampler
	}
	if action != ActionGenerate || sampler == "ddim_v3" {
		sm, smDyn = false, false
	}
	return sampler, sm, sm && smDyn, nil
}

// generationPresets resolves the quality and UC presets: qualityPresetId, or
// qualityToggle (true is standard, false none), standard by default;
// ucPresetId, or the legacy index ucPreset, heavy by default. With presetsOff
// both default to none. V5 Full Medium allows only heavy.
func generationPresets(c *gin.Context, fields requestFields, modelProfile *profile, model string, presetsOff bool) (string, string, *types.NewAPIError) {
	unsupported := func(field, value string) *types.NewAPIError {
		return badRequest(c, i18n.MsgNovelAIFieldValue, map[string]any{"Field": field, "Value": value, "Model": model})
	}
	quality, uc := presetStandard, presetHeavy
	if presetsOff {
		quality, uc = presetNone, presetNone
	}
	toggle, apiErr := fields.boolean("qualityToggle")
	if apiErr != nil {
		return "", "", apiErr
	}
	if toggle != nil {
		quality = presetNone
		if *toggle {
			quality = presetStandard
		}
	}
	requested, apiErr := fields.str("qualityPresetId")
	if apiErr != nil {
		return "", "", apiErr
	}
	if requested != "" {
		if _, ok := modelProfile.quality[requested]; !ok {
			return "", "", unsupported("qualityPresetId", requested)
		}
		quality = requested
	}

	if modelProfile.medium {
		return quality, presetHeavy, nil
	}
	index, apiErr := fields.integer("ucPreset", 0, int64(len(modelProfile.ucIndex)-1))
	if apiErr != nil {
		return "", "", apiErr
	}
	if index != nil {
		uc = modelProfile.ucIndex[*index]
	}
	requested, apiErr = fields.str("ucPresetId")
	if apiErr != nil {
		return "", "", apiErr
	}
	if requested != "" {
		if _, ok := modelProfile.uc[requested]; !ok {
			return "", "", unsupported("ucPresetId", requested)
		}
		uc = requested
	}
	return quality, uc, nil
}

// joinTags appends tags to a prompt with ", ", as the web client does.
func joinTags(prompt, tags string) string {
	switch {
	case tags == "":
		return prompt
	case prompt == "":
		return tags
	}
	return prompt + ", " + tags
}

// mixWeight is the trailing :weight of a V3 mixed prompt.
var mixWeight = regexp.MustCompile(`:-?[0-9.]+\s*$`)

// withPresetTags adds the preset tags (the transparency tag and the quality
// suffix) to a prompt where the web client puts them. From V4 on they go to
// the base prompt only, the first character field, and before a Text: block,
// whose text NovelAI draws literally: the tags then end with a period. On V3,
// whose | mixes prompts, every mixed prompt gets them before its :weight.
func withPresetTags(prompt, tags string, v3 bool) string {
	if tags == "" {
		return prompt
	}
	fields := promptFields(prompt)
	if !v3 {
		field := fields[0]
		if start := textBlockStart(field); start >= 0 {
			head := strings.TrimRightFunc(field[:start], func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == '.' })
			fields[0] = joinTags(head, tags) + ". " + field[start:]
		} else {
			head := strings.TrimRightFunc(field, unicode.IsSpace)
			fields[0] = joinTags(head, tags) + field[len(head):]
		}
		return strings.Join(fields, "|")
	}
	for i, field := range fields {
		weight := ""
		if len(fields) > 1 {
			if loc := mixWeight.FindStringIndex(field); loc != nil {
				field, weight = field[:loc[0]], field[loc[0]:]
			}
		}
		head := strings.TrimRightFunc(field, unicode.IsSpace)
		fields[i] = joinTags(head, tags) + field[len(head):] + weight
	}
	return strings.Join(fields, "|")
}

// promptFields splits a prompt at its single bars, which mix prompts on V3
// and separate character fields from V4 on. A || pair delimits a
// randomizer, whose single bars separate its alternatives.
func promptFields(prompt string) []string {
	var fields []string
	start, randomizer := 0, false
	for i := 0; i < len(prompt); i++ {
		switch {
		case strings.HasPrefix(prompt[i:], "||"):
			randomizer = !randomizer
			i++
		case prompt[i] == '|' && !randomizer:
			fields = append(fields, prompt[start:i])
			start = i + 1
		}
	}
	return append(fields, prompt[start:])
}

// textBlockStart is where the first Text: block of a V4+ prompt field
// starts, or -1. The marker is the web client's: "text:" in any case but not
// "text::", at the start or after a space or one of , . : [ ] { } | 、 。; the
// brackets that open right before it belong to the block. A block inside a
// randomizer starts where the randomizer does.
func textBlockStart(field string) int {
	randomizer := -1
	for i := 0; i < len(field); i++ {
		if strings.HasPrefix(field[i:], "||") {
			if randomizer < 0 {
				randomizer = i
			} else {
				randomizer = -1
			}
			i++
			continue
		}
		rest := field[i:]
		if len(rest) < 5 || !strings.EqualFold(rest[:5], "text:") || strings.HasPrefix(rest[5:], ":") {
			continue
		}
		if previous, _ := utf8.DecodeLastRuneInString(field[:i]); i > 0 && !unicode.IsSpace(previous) && !strings.ContainsRune(",.:[]{}|、。", previous) {
			continue
		}
		if randomizer >= 0 {
			i = randomizer
		}
		for i > 0 && (field[i-1] == '[' || field[i-1] == '{') {
			i--
		}
		return i
	}
	return -1
}

func badRequest(c *gin.Context, key string, args map[string]any) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, key, args)), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// requestFields reads the NovelAI keys of a request: parameters over the
// top-level extension keys. A JSON null counts as absent.
type requestFields struct {
	c      *gin.Context
	values map[string]json.RawMessage
}

func (f requestFields) raw(key string) (json.RawMessage, bool) {
	raw, ok := f.values[key]
	if !ok || isJSONNull(raw) {
		return nil, false
	}
	return raw, true
}

func (f requestFields) invalid(key string) *types.NewAPIError {
	return badRequest(f.c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": key})
}

func (f requestFields) str(key string) (string, *types.NewAPIError) {
	raw, ok := f.raw(key)
	if !ok {
		return "", nil
	}
	var value string
	if common.Unmarshal(raw, &value) != nil {
		return "", f.invalid(key)
	}
	return value, nil
}

// choice reads a string that must be one of values ("" when absent).
func (f requestFields) choice(key string, values []string, model string) (string, *types.NewAPIError) {
	value, err := f.str(key)
	if err != nil || value == "" {
		return "", err
	}
	if !slices.Contains(values, value) {
		return "", badRequest(f.c, i18n.MsgNovelAIFieldValue, map[string]any{"Field": key, "Value": value, "Model": model})
	}
	return value, nil
}

func (f requestFields) boolean(key string) (*bool, *types.NewAPIError) {
	raw, ok := f.raw(key)
	if !ok {
		return nil, nil
	}
	var value bool
	if common.Unmarshal(raw, &value) != nil {
		return nil, f.invalid(key)
	}
	return &value, nil
}

func (f requestFields) number(key string, minimum, maximum float64) (*float64, *types.NewAPIError) {
	raw, ok := f.raw(key)
	if !ok {
		return nil, nil
	}
	var value float64
	if common.Unmarshal(raw, &value) != nil || math.IsNaN(value) || value < minimum || value > maximum {
		return nil, badRequest(f.c, i18n.MsgNovelAIFieldRange, map[string]any{"Field": key, "Min": minimum, "Max": maximum})
	}
	return &value, nil
}

func (f requestFields) integer(key string, minimum, maximum int64) (*int64, *types.NewAPIError) {
	raw, ok := f.raw(key)
	if !ok {
		return nil, nil
	}
	value, isInteger := jsonInteger(raw)
	if !isInteger || value < minimum || value > maximum {
		return nil, badRequest(f.c, i18n.MsgNovelAIFieldRange, map[string]any{"Field": key, "Min": minimum, "Max": maximum})
	}
	return &value, nil
}

// varietySigma is skip_cfg_above_sigma: the request's number (or null), or
// with variety true NovelAI's Variety+ value for the canvas, which grows
// with the square root of its area; null (off) by default.
func (f requestFields) varietySigma(modelProfile *profile, width, height int) (any, *types.NewAPIError) {
	if raw, ok := f.raw("skip_cfg_above_sigma"); ok {
		var value float64
		if common.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, f.invalid("skip_cfg_above_sigma")
		}
		return value, nil
	}
	variety, err := f.boolean("variety")
	if err != nil || variety == nil || !*variety {
		return nil, err
	}
	return modelProfile.varietySigma * math.Sqrt(float64(width*height)/varietyArea), nil
}

// aspectSize is the canvas of the extension aspect_ratio "W:H" (0, 0 when
// absent): its aspectPresets canvas, or areaSize at about 1024x1024.
func (f requestFields) aspectSize() (int, int, *types.NewAPIError) {
	value, err := f.str("aspect_ratio")
	if err != nil || value == "" || strings.EqualFold(value, "auto") {
		return 0, 0, err
	}
	w, h, ok := strings.Cut(value, ":")
	width, errW := strconv.ParseFloat(strings.TrimSpace(w), 64)
	height, errH := strconv.ParseFloat(strings.TrimSpace(h), 64)
	if !ok || errW != nil || errH != nil || !(width > 0) || !(height > 0) || width/height > 64 || height/width > 64 {
		return 0, 0, f.invalid("aspect_ratio")
	}
	if preset, ok := aspectPresets[strconv.FormatFloat(width, 'f', -1, 64)+":"+strconv.FormatFloat(height, 'f', -1, 64)]; ok {
		return preset[0], preset[1], nil
	}
	canvasWidth, canvasHeight := areaSize(width/height, autoEditPixels)
	return canvasWidth, canvasHeight, nil
}

// explicitCaption is a V4 caption object the request sends itself.
type explicitCaption struct {
	raw         json.RawMessage
	baseCaption string
}

// caption reads an explicit v4_prompt / v4_negative_prompt, which needs a
// string caption.base_caption. V3 has no V4 captions, so it ignores them.
func (f requestFields) caption(key string, modelProfile *profile) (*explicitCaption, *types.NewAPIError) {
	raw, ok := f.raw(key)
	if !ok || modelProfile.family == familyV3 {
		return nil, nil
	}
	var value struct {
		Caption *struct {
			BaseCaption *string `json:"base_caption"`
		} `json:"caption"`
	}
	if common.Unmarshal(raw, &value) != nil || value.Caption == nil || value.Caption.BaseCaption == nil {
		return nil, f.invalid(key)
	}
	return &explicitCaption{raw: raw, baseCaption: *value.Caption.BaseCaption}, nil
}

type characterPrompt struct {
	prompt, uc string
	x, y       float64
	positioned bool
}

// characterGrid is where V4 and V4.5 place a character; V5 takes any point.
var characterGrid = []float64{0.1, 0.3, 0.5, 0.7, 0.9}

// characters reads characterPrompts, [{prompt, uc?, center?:{x,y},
// enabled?}], the web client's shape (the extension characters is the same
// list). Disabled entries are left out. A center other than 0,0 positions
// the character; V4 and V4.5 snap it to their grid.
func (f requestFields) characters(modelProfile *profile, model string) ([]characterPrompt, *types.NewAPIError) {
	raw, ok := f.raw("characterPrompts")
	if !ok {
		return nil, nil
	}
	var entries []struct {
		Prompt  *string `json:"prompt"`
		UC      *string `json:"uc"`
		Enabled *bool   `json:"enabled"`
		Center  *struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		} `json:"center"`
	}
	if common.Unmarshal(raw, &entries) != nil {
		return nil, f.invalid("characterPrompts")
	}
	var characters []characterPrompt
	for _, entry := range entries {
		if entry.Prompt == nil {
			return nil, f.invalid("characterPrompts")
		}
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		character := characterPrompt{prompt: *entry.Prompt}
		if entry.UC != nil {
			character.uc = *entry.UC
		}
		if entry.Center != nil {
			if entry.Center.X == nil || entry.Center.Y == nil || *entry.Center.X < 0 || *entry.Center.X > 1 || *entry.Center.Y < 0 || *entry.Center.Y > 1 {
				return nil, f.invalid("characterPrompts")
			}
			character.x, character.y = *entry.Center.X, *entry.Center.Y
			character.positioned = character.x != 0 || character.y != 0
		}
		if character.positioned && modelProfile.family != familyV5 {
			character.x, character.y = snapToGrid(character.x), snapToGrid(character.y)
		}
		characters = append(characters, character)
	}
	if len(characters) > modelProfile.maxCharacters {
		return nil, badRequest(f.c, i18n.MsgNovelAICharactersTooMany, map[string]any{"Model": model, "Max": modelProfile.maxCharacters})
	}
	return characters, nil
}

func snapToGrid(value float64) float64 {
	best := characterGrid[0]
	for _, candidate := range characterGrid[1:] {
		if math.Abs(candidate-value) < math.Abs(best-value) {
			best = candidate
		}
	}
	return best
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// jsonInteger reads a JSON number that is a whole number within int64.
func jsonInteger(raw json.RawMessage) (int64, bool) {
	var number json.Number
	if common.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	if value, err := number.Int64(); err == nil {
		return value, true
	}
	value, err := number.Float64()
	if err != nil || value != math.Trunc(value) || math.Abs(value) > 1<<53 {
		return 0, false
	}
	return int64(value), true
}
