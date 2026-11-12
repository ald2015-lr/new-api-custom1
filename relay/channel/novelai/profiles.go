package novelai

// family is a NovelAI model generation: the request shape, samplers, noise
// schedules and prompt features follow it.
type family int

const (
	familyV3 family = iota
	familyV4
	familyV45
	familyV5
)

// Preset ids, as the NovelAI web client sends them in qualityPresetId and
// ucPresetId.
const (
	presetStandard   = "standard"
	presetLight      = "light"
	presetNone       = "none"
	presetHeavy      = "heavy"
	presetHumanFocus = "humanFocus"
	presetFurryFocus = "furryFocus"
)

// tagHintQuality and tagHintUC are the web client's numeric hints for the
// quality and UC presets (tag_hint_qt, tag_hint_uc_preset). NovelAI keeps
// them as metadata.
var (
	tagHintQuality = map[string]int{presetNone: 0, presetStandard: 1, presetLight: 3}
	tagHintUC      = map[string]int{presetNone: 0, presetHeavy: 2, presetLight: 3, presetHumanFocus: 4, presetFurryFocus: 5}
)

// profile is what the relay knows of one NovelAI image model. NovelAI does
// not expand presets itself: the quality suffix and the UC (negative) preset
// text must already be in the prompts the relay sends.
type profile struct {
	family family
	// curated models get no "nsfw, " in front of the UC preset.
	curated bool
	// medium is V5 Full at Effort Medium: 14 steps, k_euler_ancestral, no
	// cfg_rescale and exactly the heavy UC are fixed.
	medium bool
	scale  float64
	// inpainting is the model action "infill" runs on. NovelAI has no V5
	// Curated inpainting model; its web client inpaints V5 Curated with V4.5
	// Curated's.
	inpainting string
	// quality maps a quality preset id to the tags appended to the prompt.
	quality map[string]string
	// uc maps a UC preset id to the text put in front of the negative prompt.
	uc map[string]string
	// ucIndex is the order of the legacy numeric ucPreset field.
	ucIndex       []string
	maxCharacters int
	// varietySigma is the Variety+ coefficient of skip_cfg_above_sigma, 0
	// where Variety+ does not exist.
	varietySigma float64
}

// Quality suffixes and UC preset texts of the NovelAI web client (Oct-Nov
// 2026), as novelai-bridge's src/model/presets.rs records them.
const (
	qualityV5Standard = "very aesthetic, masterpiece, no text"
	qualityV5Light    = "very aesthetic, amazing quality, no text"
	qualityV45Curated = "very aesthetic, masterpiece, no text, -0.8::feet::, rating:general"
	qualityV4Full     = "no text, best quality, very aesthetic, absurdres"
	qualityV4Curated  = "rating:general, best quality, very aesthetic, absurdres"
	qualityV3         = "best quality, amazing quality, very aesthetic, absurdres"
	qualityFurry3     = "{best quality}, {amazing quality}"

	ucV5Heavy        = "lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page"
	ucV5Light        = "lowres, bad hands, bad anatomy, artistic error, sepia, white haze, worst quality, very displeasing, jpeg artifacts, 0::ai-generated::"
	ucV5HumanFocus   = ucV5Heavy + ", @_@, mismatched pupils, glowing eyes, bad anatomy"
	ucV5FurryFocus   = "{worst quality}, distracting watermark, unfinished, bad quality, {widescreen}, upscale, {sequence}, {{grandfathered content}}, blurred foreground, chromatic aberration, sketch, everyone, [sketch background], simple, [flat colors], ych (character), outline, multiple scenes, [[horror (theme)]], comic"
	ucV45FullLight   = "lowres, artistic error, scan artifacts, worst quality, bad quality, jpeg artifacts, multiple views, very displeasing, too many watermarks, negative space, blank page"
	ucV45CurHeavy    = "blurry, lowres, upscaled, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, halftone, multiple views, logo, too many watermarks, negative space, blank page"
	ucV45CurLight    = "blurry, lowres, upscaled, artistic error, scan artifacts, jpeg artifacts, logo, too many watermarks, negative space, blank page"
	ucV45CurHuman    = "blurry, lowres, upscaled, artistic error, film grain, scan artifacts, bad anatomy, bad hands, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, halftone, multiple views, logo, too many watermarks, @_@, mismatched pupils, glowing eyes, negative space, blank page"
	ucV4FullHeavy    = "blurry, lowres, error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, multiple views, logo, too many watermarks, white blank page, blank page"
	ucV4FullLight    = "blurry, lowres, error, worst quality, bad quality, jpeg artifacts, very displeasing, white blank page, blank page"
	ucV4CurHeavy     = "blurry, lowres, error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, logo, dated, signature, multiple views, gigantic breasts, white blank page, blank page"
	ucV4CurLight     = "blurry, lowres, error, worst quality, bad quality, jpeg artifacts, very displeasing, logo, dated, signature, white blank page, blank page"
	ucV3Heavy        = "lowres, {bad}, error, fewer, extra, missing, worst quality, jpeg artifacts, bad quality, watermark, unfinished, displeasing, chromatic aberration, signature, extra digits, artistic error, username, scan, [abstract]"
	ucV3Light        = "lowres, jpeg artifacts, worst quality, watermark, blurry, very displeasing"
	ucV3HumanFocus   = ucV3Heavy + ", bad anatomy, bad hands, @_@, mismatched pupils, heart-shaped pupils, glowing eyes"
	ucFurry3Heavy    = "{{worst quality}}, [displeasing], {unusual pupils}, guide lines, {{unfinished}}, {bad}, url, artist name, {{tall image}}, mosaic, {sketch page}, comic panel, impact (font), [dated], {logo}, ych, {what}, {where is your god now}, {distorted text}, repeated text, {floating head}, {1994}, {widescreen}, absolutely everyone, sequence, {compression artifacts}, hard translated, {cropped}, {commissioner name}, unknown text, high contrast"
	ucFurry3Light    = "{worst quality}, guide lines, unfinished, bad, url, tall image, widescreen, compression artifacts, unknown text"
	ucV3NoneFallback = "lowres"
)

var (
	qualityV5 = map[string]string{presetStandard: qualityV5Standard, presetLight: qualityV5Light, presetNone: ""}
	ucV5      = map[string]string{presetHeavy: ucV5Heavy, presetLight: ucV5Light, presetFurryFocus: ucV5FurryFocus, presetHumanFocus: ucV5HumanFocus, presetNone: ""}
	// ucIndexV5 is also V4.5 Full's order.
	ucIndexV5 = []string{presetHeavy, presetLight, presetFurryFocus, presetHumanFocus, presetNone}

	profileV5Full = &profile{
		family: familyV5, scale: 7, inpainting: "nai-diffusion-5-full-inpainting",
		quality: qualityV5, uc: ucV5, ucIndex: ucIndexV5, maxCharacters: 32,
	}
	profileV5Medium = &profile{
		family: familyV5, medium: true, scale: 7, inpainting: "nai-diffusion-5-full-medium-inpainting",
		quality: qualityV5, uc: map[string]string{presetHeavy: ucV5Heavy}, ucIndex: []string{presetHeavy}, maxCharacters: 32,
	}
	profileV5Curated = &profile{
		family: familyV5, curated: true, scale: 7, inpainting: "nai-diffusion-4-5-curated-inpainting",
		quality: qualityV5, uc: ucV5, ucIndex: ucIndexV5, maxCharacters: 32,
	}
	profileV45Full = &profile{
		family: familyV45, scale: 5, inpainting: "nai-diffusion-4-5-full-inpainting",
		quality: map[string]string{presetStandard: qualityV5Standard, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucV5Heavy, presetLight: ucV45FullLight, presetFurryFocus: ucV5FurryFocus, presetHumanFocus: ucV5HumanFocus, presetNone: ""},
		ucIndex: ucIndexV5, maxCharacters: 6, varietySigma: 58,
	}
	profileV45Curated = &profile{
		family: familyV45, curated: true, scale: 5, inpainting: "nai-diffusion-4-5-curated-inpainting",
		quality: map[string]string{presetStandard: qualityV45Curated, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucV45CurHeavy, presetLight: ucV45CurLight, presetHumanFocus: ucV45CurHuman, presetNone: ""},
		ucIndex: []string{presetHeavy, presetLight, presetHumanFocus, presetNone}, maxCharacters: 6, varietySigma: 58,
	}
	profileV4Full = &profile{
		family: familyV4, scale: 5.5, inpainting: "nai-diffusion-4-full-inpainting",
		quality: map[string]string{presetStandard: qualityV4Full, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucV4FullHeavy, presetLight: ucV4FullLight, presetNone: ""},
		ucIndex: []string{presetHeavy, presetLight, presetNone}, maxCharacters: 6, varietySigma: 19,
	}
	profileV4Curated = &profile{
		family: familyV4, curated: true, scale: 5.5, inpainting: "nai-diffusion-4-curated-inpainting",
		quality: map[string]string{presetStandard: qualityV4Curated, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucV4CurHeavy, presetLight: ucV4CurLight, presetNone: ""},
		ucIndex: []string{presetHeavy, presetLight, presetNone}, maxCharacters: 6, varietySigma: 19,
	}
	profileV3 = &profile{
		family: familyV3, scale: 5, inpainting: "nai-diffusion-3-inpainting",
		quality: map[string]string{presetStandard: qualityV3, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucV3Heavy, presetLight: ucV3Light, presetHumanFocus: ucV3HumanFocus, presetNone: ucV3NoneFallback},
		ucIndex: []string{presetHeavy, presetLight, presetHumanFocus, presetNone}, varietySigma: 19,
	}
	profileFurry3 = &profile{
		family: familyV3, scale: 6.2, inpainting: "nai-diffusion-furry-3-inpainting",
		quality: map[string]string{presetStandard: qualityFurry3, presetNone: ""},
		uc:      map[string]string{presetHeavy: ucFurry3Heavy, presetLight: ucFurry3Light, presetNone: ucV3NoneFallback},
		ucIndex: []string{presetHeavy, presetLight, presetNone}, varietySigma: 19,
	}
)

// profiles maps every model of ModelList to its profile. An inpainting model
// has the profile of the model it inpaints for; nai-diffusion-4-5-curated-
// inpainting, which also inpaints V5 Curated, follows V4.5 Curated's rules.
var profiles = map[string]*profile{
	"nai-diffusion-5-full":                   profileV5Full,
	"nai-diffusion-5-full-inpainting":        profileV5Full,
	"nai-diffusion-5-full-medium":            profileV5Medium,
	"nai-diffusion-5-full-medium-inpainting": profileV5Medium,
	"nai-diffusion-5-curated":                profileV5Curated,
	"nai-diffusion-4-5-full":                 profileV45Full,
	"nai-diffusion-4-5-full-inpainting":      profileV45Full,
	"nai-diffusion-4-5-curated":              profileV45Curated,
	"nai-diffusion-4-5-curated-inpainting":   profileV45Curated,
	"nai-diffusion-4-full":                   profileV4Full,
	"nai-diffusion-4-full-inpainting":        profileV4Full,
	"nai-diffusion-4-curated-preview":        profileV4Curated,
	"nai-diffusion-4-curated-inpainting":     profileV4Curated,
	"nai-diffusion-3":                        profileV3,
	"nai-diffusion-3-inpainting":             profileV3,
	"nai-diffusion-furry-3":                  profileFurry3,
	"nai-diffusion-furry-3-inpainting":       profileFurry3,
}

// inpaintingModels are NovelAI's inpainting models, which serve only action
// "infill".
var inpaintingModels = map[string]bool{
	"nai-diffusion-5-full-inpainting":        true,
	"nai-diffusion-5-full-medium-inpainting": true,
	"nai-diffusion-4-5-full-inpainting":      true,
	"nai-diffusion-4-5-curated-inpainting":   true,
	"nai-diffusion-4-full-inpainting":        true,
	"nai-diffusion-4-curated-inpainting":     true,
	"nai-diffusion-3-inpainting":             true,
	"nai-diffusion-furry-3-inpainting":       true,
}

// Samplers each family accepts, as the web client offers them. ddim is
// accepted as an alias (ddim_v3 on V3, k_euler_ancestral after), and on V3
// nai_smea / nai_smea_dyn are k_euler_ancestral with SMEA (and DYN).
var (
	samplersV3 = []string{"k_euler", "k_euler_ancestral", "k_dpmpp_2s_ancestral", "k_dpmpp_2m", "k_dpmpp_sde", "k_dpmpp_2m_sde", "ddim_v3"}
	samplersV4 = []string{"k_euler_ancestral", "k_euler", "k_dpmpp_2s_ancestral", "k_dpmpp_2m_sde", "k_dpmpp_2m", "k_dpmpp_sde"}
)

var noiseSchedules = []string{"native", "karras", "exponential", "polyexponential"}
