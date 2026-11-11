package novelai

const ChannelName = "novelai"

// ModelList is the image models NovelAI's API serves. The *-inpainting models
// take the action "infill"; V5 Curated has none and inpaints with
// nai-diffusion-4-5-curated-inpainting.
var ModelList = []string{
	"nai-diffusion-5-full",
	"nai-diffusion-5-full-medium",
	"nai-diffusion-5-curated",
	"nai-diffusion-4-5-full",
	"nai-diffusion-4-5-curated",
	"nai-diffusion-4-full",
	"nai-diffusion-4-curated-preview",
	"nai-diffusion-3",
	"nai-diffusion-furry-3",
	"nai-diffusion-5-full-inpainting",
	"nai-diffusion-5-full-medium-inpainting",
	"nai-diffusion-4-5-full-inpainting",
	"nai-diffusion-4-5-curated-inpainting",
	"nai-diffusion-4-full-inpainting",
	"nai-diffusion-4-curated-inpainting",
	"nai-diffusion-3-inpainting",
	"nai-diffusion-furry-3-inpainting",
}
