package novelai

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

// extensionKeys are the NovelAI keys an OpenAI-format request may carry at
// its top level, which also holds other clients' keys (user, moderation,
// ...): NovelAI's names, and aliases other NovelAI relays accept. Any other
// top-level key is ignored; the parameters object takes NovelAI keys
// without a list.
var extensionKeys = map[string]string{
	"negative_prompt":      "negative_prompt",
	"negative":             "negative_prompt",
	"seed":                 "seed",
	"steps":                "steps",
	"scale":                "scale",
	"cfg_rescale":          "cfg_rescale",
	"sampler":              "sampler",
	"noise_schedule":       "noise_schedule",
	"sm":                   "sm",
	"sm_dyn":               "sm_dyn",
	"ucPreset":             "ucPreset",
	"ucPresetId":           "ucPresetId",
	"uc_preset":            "ucPresetId",
	"qualityToggle":        "qualityToggle",
	"qualityPresetId":      "qualityPresetId",
	"strength":             "strength",
	"noise":                "noise",
	"characterPrompts":     "characterPrompts",
	"characters":           "characterPrompts",
	"skip_cfg_above_sigma": "skip_cfg_above_sigma",
	"variety":              "variety",
	"image_format":         "image_format",
	"params_version":       "params_version",
	"legacy_uc":            "legacy_uc",
	"aspect_ratio":         "aspect_ratio",
	"hd":                   "hd",
	"upscale":              "upscale",
}

// stringKeys are the extension keys whose value is a string, which a
// multipart form sends as is; the other form values are JSON (numbers,
// booleans, lists).
var stringKeys = map[string]bool{
	"negative_prompt": true, "negative": true, "sampler": true, "noise_schedule": true,
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
// FormFields): the extension keys under their NovelAI names (an alias
// counts only when the NovelAI name is absent; image_config.aspect_ratio,
// OpenRouter's name, when aspect_ratio is), and the parameters object.
func ParseExtensions(c *gin.Context, fields map[string]json.RawMessage) (map[string]json.RawMessage, map[string]json.RawMessage, *types.NewAPIError) {
	for _, key := range ambiguousKeys {
		if _, ok := fields[key]; ok {
			return nil, nil, badRequest(c, i18n.MsgNovelAIFieldUnsupported, map[string]any{"Field": key})
		}
	}
	extras := map[string]json.RawMessage{}
	for key, value := range fields {
		canonical, ok := extensionKeys[key]
		if !ok {
			continue
		}
		if _, set := fields[canonical]; set && canonical != key {
			continue
		}
		extras[canonical] = value
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

// mergeNestedFields adds to fields the keys of the object fields[key] it does
// not set itself: clients nest provider keys in extra_body (chat) or
// extra_fields (images, new-api's convention), which the top level overrides.
func mergeNestedFields(c *gin.Context, fields map[string]json.RawMessage, key string) *types.NewAPIError {
	raw, ok := fields[key]
	if !ok || common.GetJsonType(raw) != "object" {
		return nil
	}
	var nested map[string]json.RawMessage
	if common.Unmarshal(raw, &nested) != nil {
		return badRequest(c, i18n.MsgNovelAIFieldInvalid, map[string]any{"Field": key})
	}
	for name, value := range nested {
		if _, set := fields[name]; !set {
			fields[name] = value
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
