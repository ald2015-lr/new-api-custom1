package novelai

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

// extensionKeys are the NovelAI keys an OpenAI-format request may carry at
// its top level, which also holds other clients' keys (user, moderation,
// ...), under their NovelAI names; width and height set the canvas as
// parameters.width / height do. Any other top-level key is ignored but for
// extensionAliases; the parameters object takes NovelAI keys without a list.
var extensionKeys = []string{
	"negative_prompt", "seed", "steps", "scale", "cfg_rescale", "sampler", "noise_schedule", "sm", "sm_dyn",
	"ucPreset", "ucPresetId", "qualityToggle", "qualityPresetId", "strength", "noise", "characterPrompts",
	"skip_cfg_above_sigma", "variety", "image_format", "params_version", "legacy_uc", "aspect_ratio",
	"width", "height", "hd", "upscale",
}

// extensionAliases are the names other NovelAI relays and image clients give
// extension keys, in precedence order: a key's NovelAI name wins over its
// aliases, and an earlier alias over a later one.
var extensionAliases = []struct{ name, key string }{
	{"negative", "negative_prompt"},
	{"negativePrompt", "negative_prompt"},
	{"uc", "negative_prompt"},
	{"num_inference_steps", "steps"},
	{"cfg_scale", "scale"},
	{"guidance_scale", "scale"},
	{"uc_preset", "ucPresetId"},
	{"characters", "characterPrompts"},
}

// aliasRanges are NovelAI's ranges of the extension keys whose aliases carry
// other clients' numbers, which range wider: their guidance and step counts.
var aliasRanges = map[string][2]float64{"steps": {1, maxSteps}, "scale": {0, maxScale}}

// stringKeys are the extension keys whose value is a string, which a
// multipart form sends as is; the other form values are JSON (numbers,
// booleans, lists).
var stringKeys = map[string]bool{
	"negative_prompt": true, "negative": true, "negativePrompt": true, "uc": true, "sampler": true, "noise_schedule": true,
	"ucPresetId": true, "uc_preset": true, "qualityPresetId": true, "image_format": true, "aspect_ratio": true,
	"prompt": true, "model": true, "size": true, "quality": true, "response_format": true,
	"background": true, "output_format": true, "moderation": true, "user": true, "input_fidelity": true,
	"image": true, "mask": true,
}

// ambiguousKeys are top-level keys some NovelAI relays accept with a meaning
// nobody has confirmed (cfg could be scale or cfg_rescale); they are
// rejected rather than guessed.
var ambiguousKeys = []string{"cfg", "SMEA", "DYN", "addition"}

// ParseExtensions picks the NovelAI keys out of an OpenAI-format request's
// top-level fields (the JSON body, or a multipart form through
// FormFields): the extension keys under their NovelAI names, or else under
// their first alias the request sets (image_config.aspect_ratio,
// OpenRouter's name, when aspect_ratio is absent), and the parameters
// object. A null counts as absent: a later alias replaces it. An alias's
// number of steps or guidance is brought into NovelAI's range (aliasRanges),
// a step count to a whole number, as size is fitted to NovelAI's grid,
// rather than refused: other clients' ranges are wider.
func ParseExtensions(c *gin.Context, fields map[string]json.RawMessage) (map[string]json.RawMessage, map[string]json.RawMessage, *types.NewAPIError) {
	for _, key := range ambiguousKeys {
		if _, ok := fields[key]; ok {
			return nil, nil, badRequest(c, i18n.MsgNovelAIFieldUnsupported, map[string]any{"Field": key})
		}
	}
	extras := map[string]json.RawMessage{}
	for _, key := range extensionKeys {
		if value, ok := fields[key]; ok {
			extras[key] = value
		}
	}
	for _, alias := range extensionAliases {
		value, ok := fields[alias.name]
		if current, set := extras[alias.key]; !ok || set && !isJSONNull(current) {
			continue
		}
		if limits, ranged := aliasRanges[alias.key]; ranged {
			if number, ok := jsonNumber(value); ok {
				number = min(max(number, limits[0]), limits[1])
				if alias.key == "steps" {
					number = math.Round(number)
				}
				if fitted, err := common.Marshal(number); err == nil {
					value = fitted
				}
			}
		}
		extras[alias.key] = value
	}
	if raw, ok := fields["image_config"]; ok && extras["aspect_ratio"] == nil && common.GetJsonType(raw) == "object" {
		var imageConfig struct {
			AspectRatio json.RawMessage `json:"aspect_ratio"`
		}
		if common.Unmarshal(raw, &imageConfig) != nil {
			return nil, nil, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "image_config"})
		}
		if imageConfig.AspectRatio != nil {
			extras["aspect_ratio"] = imageConfig.AspectRatio
		}
	}
	var parameters map[string]json.RawMessage
	if raw, ok := fields["parameters"]; ok && !isJSONNull(raw) {
		if common.Unmarshal(raw, &parameters) != nil || parameters == nil {
			return nil, nil, badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": "parameters"})
		}
	}
	return extras, parameters, nil
}

// mergeNestedFields adds to fields the keys of the objects fields[keys[i]]
// that it does not set itself, or sets to null: clients nest provider keys
// in extra_body (the OpenAI SDK's name, which chat clients use) or
// extra_fields (new-api's, which images clients use). The top level wins,
// then the objects in the order of keys.
func mergeNestedFields(c *gin.Context, fields map[string]json.RawMessage, keys ...string) *types.NewAPIError {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok || common.GetJsonType(raw) != "object" {
			continue
		}
		var nested map[string]json.RawMessage
		if common.Unmarshal(raw, &nested) != nil {
			return badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": key})
		}
		for name, value := range nested {
			if current, set := fields[name]; !set || isJSONNull(current) {
				fields[name] = value
			}
		}
	}
	return nil
}

// FormFields turns multipart form values into the JSON fields of an
// equivalent JSON request: strings for string keys, JSON for the others
// (a value that is not JSON stays a string, and fails its key's type
// check). The parameters field is a JSON object, or SDK-style bracket keys
// parameters[steps]=28. The first value of a repeated field counts.
func FormFields(values map[string][]string) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	parameters := map[string]json.RawMessage{}
	formValue := func(key, value string) json.RawMessage {
		if !stringKeys[key] {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" && ojson.ValidBytes([]byte(trimmed)) {
				return json.RawMessage(trimmed)
			}
		}
		encoded, _ := common.Marshal(value)
		return encoded
	}
	for key, list := range values {
		if len(list) == 0 {
			continue
		}
		if name, ok := strings.CutPrefix(key, "parameters["); ok && strings.HasSuffix(name, "]") {
			name = strings.TrimSuffix(name, "]")
			parameters[name] = formValue(name, list[0])
			continue
		}
		if key == "parameters" {
			fields[key] = json.RawMessage(list[0])
			continue
		}
		fields[key] = formValue(key, list[0])
	}
	if _, ok := fields["parameters"]; !ok && len(parameters) > 0 {
		fields["parameters"], _ = common.Marshal(parameters)
	}
	return fields
}
