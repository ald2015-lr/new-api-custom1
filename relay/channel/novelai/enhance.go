package novelai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// HD is NovelAI's Enhance, as its web client runs it, done by the relay for
// OpenAI-format text-to-image requests on V4 and later (Generation.HD): once
// the base images arrived, each one goes back to /ai/generate-image as an
// img2img at 1.5 times its size (hdCanvas), with the request as it was sent
// and only the enhance keys changed (hdPlan.requestBody). The user's price
// is the base request's: the image count and the usage tokens do not change.
// An enhance that fails in any way, NovelAI's 402 for an account without
// Anlas included, delivers the base image and ends HD for the rest of the
// request; a 402 also skips HD on the key for hdCooldown. The key stays held
// from the base send through the last enhance answer: one request is one job
// on the key.

const (
	// hdMaxAspectDrift bounds how much rounding the HD canvas to multiples
	// of 64 may stretch one side against the other.
	hdMaxAspectDrift = 0.03
	// hdStrength and hdNoise are the web client's Enhance at its default
	// magnitude.
	hdStrength = 0.5
	hdNoise    = 0
	// hdPromptAddition is what NovelAI's client adds to an enhance prompt on
	// V4.5 and V5.
	hdPromptAddition = ", -2::upscaled, blurry::,"
	// hdBodyLimit bounds the sent request HD plans from; a text-to-image
	// body is a few KB.
	hdBodyLimit = 1 << 20
)

var (
	// enhanceTimeout bounds one enhance call, hdBudget all the enhance work
	// of a request from the start of its first preparation, hdCooldown how
	// long a 402 skips HD on a key. Tests change them.
	enhanceTimeout = 60 * time.Second
	hdBudget       = 120 * time.Second
	hdCooldown     = 10 * time.Minute
	// hdCannotPay holds, by the SHA-256 of the key, the hdRefusal of each
	// key whose enhance NovelAI answered with 402.
	hdCannotPay sync.Map
)

// hdCanvas is the enhance canvas of a width x height generation: each side
// times 1.5 rounded down, as NovelAI's client resizes the source, then to
// the nearest multiple of 64 with ties up (JavaScript's Math.round), as its
// request goes out. ok is false past NovelAI's area, or when rounding one
// side stretches the image against the other by more than hdMaxAspectDrift.
func hdCanvas(width, height int) (w, h int, ok bool) {
	if width < 1 || height < 1 {
		return 0, 0, false
	}
	w, h = max(64, (width*3/2+32)/64*64), max(64, (height*3/2+32)/64*64)
	drift := math.Abs(float64(w)*float64(height)/(float64(h)*float64(width)) - 1)
	return w, h, w*h <= maxPixels && drift <= hdMaxAspectDrift
}

// hdReferenceLists are the parameters that carry vibe transfer and Precise
// Reference images, which an enhance would encode again.
var hdReferenceLists = []string{"reference_image_multiple", "director_reference_images", "reference_image_multiple_cached", "director_reference_images_cached"}

// hasReferenceImages tells whether parameters hold a reference list that is
// not empty.
func hasReferenceImages(parameters map[string]json.RawMessage) bool {
	for _, key := range hdReferenceLists {
		raw, ok := parameters[key]
		if !ok || isJSONNull(raw) {
			continue
		}
		var list []json.RawMessage
		if common.Unmarshal(raw, &list) != nil || len(list) > 0 {
			return true
		}
	}
	return false
}

// hdEligible says why a generation gets no HD, "" when it qualifies: a
// text-to-image request on V4 or later, opaque, without reference images,
// whose canvas has an HD canvas.
func hdEligible(generation *Generation, modelProfile *profile, parameters map[string]json.RawMessage) string {
	switch {
	case generation.Action != ActionGenerate:
		return "an edit"
	case modelProfile.family == familyV3:
		return "a V3 model"
	case generation.Transparent:
		return "a transparent background"
	case hasReferenceImages(parameters):
		return "reference images"
	}
	if _, _, ok := hdCanvas(generation.Width, generation.Height); !ok {
		return "no HD canvas for " + generation.Size()
	}
	return ""
}

// hdPrompt adds NovelAI's enhance addition as its web client does: unless the
// prompt already contains "upscaled, blurry", the raw text goes in at the
// first Text: section (the match starts at the character before "text:") or
// at the end. A section is "text:" in any ASCII case, not "text::", at the
// start or after a space or one of , . : [ ] { } 、 。.
func hdPrompt(prompt string) string {
	if strings.Contains(prompt, "upscaled, blurry") {
		return prompt
	}
	for i := 0; i+5 <= len(prompt); i++ {
		if !strings.EqualFold(prompt[i:i+5], "text:") || strings.HasPrefix(prompt[i+5:], ":") {
			continue
		}
		if i == 0 {
			return hdPromptAddition + prompt
		}
		previous, size := utf8.DecodeLastRuneInString(prompt[:i])
		if unicode.IsSpace(previous) || strings.ContainsRune(",.:[]{}、。", previous) {
			return prompt[:i-size] + hdPromptAddition + prompt[i-size:]
		}
	}
	return prompt + hdPromptAddition
}

// enhanceAnlas is NovelAI's price of one enhance at hdStrength, as its web
// client computes it (pixels = W x H of the request). It is not a billing
// quantity: the logs show it, so that operators see what HD costs them.
func enhanceAnlas(pixels, steps int, v5, medium bool) int {
	stepFactor := 1.0
	if medium {
		stepFactor = 1 / 1.06521739
	}
	base := math.Ceil(2.951823174884865e-6*float64(pixels) + 5.753298233447344e-7*float64(pixels)*float64(steps)*stepFactor)
	if v5 {
		base *= 1.5
	}
	return max(int(math.Ceil(base*hdStrength-1e-9)), 2)
}

// hdSeed is the enhance seed of an image whose own seed is seed: SplitMix64's
// finaliser of it, in [2, MaxSeed] and never seed itself. The enhance never
// reuses the image's noise ("diff the seed"), and the same request and seed
// give the same HD image. Tests may replace it.
var hdSeed = func(seed int64) int64 {
	z := uint64(seed) + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	derived := int64(z%uint64(MaxSeed-1)) + 2
	if derived == seed {
		derived = (derived-1)%(MaxSeed-1) + 2
	}
	return derived
}

// hdPlan is the HD pass of a base request as it was sent: its top level and
// parameters, raw, which every enhance copies but for the enhance keys.
type hdPlan struct {
	body       map[string]json.RawMessage
	parameters map[string]json.RawMessage
	model      string
	input      string
	// seed is the first image's seed as sent; image i has seed + i.
	seed                  int64
	baseWidth, baseHeight int
	width, height         int
	// promptAdd is true on V4.5 and V5, which get hdPromptAddition.
	promptAdd  bool
	steps      int
	v5, medium bool
	// varietySigma is the model's Variety+ coefficient, 0 on V5.
	varietySigma float64
}

// canvas is the HD canvas in OpenAI's WIDTHxHEIGHT form.
func (p *hdPlan) canvas() string {
	return strconv.Itoa(p.width) + "x" + strconv.Itoa(p.height)
}

// anlas is about what one enhance of the plan costs the account.
func (p *hdPlan) anlas() int {
	return enhanceAnlas(p.width*p.height, p.steps, p.v5, p.medium)
}

// planHD is the HD pass of the base request requestBody, the body exactly as
// it goes to NovelAI (after the channel's parameter override), or nil and
// why not. The request is checked again on that body, which an override may
// have changed: a text-to-image generation of a V4+ model on a canvas of
// multiples of 64 that has an HD canvas, with the n_samples the request
// reserved, no source image, mask or reference images, and opaque. A body
// that cannot be read again (not a common.ReplayableBody) or past
// hdBodyLimit gets no HD: the enhance must start from what NovelAI
// received.
func planHD(requestBody io.Reader, generation *Generation) (*hdPlan, string) {
	replayable, ok := requestBody.(common.ReplayableBody)
	if !ok {
		return nil, "body not replayable"
	}
	if replayable.Size() > hdBodyLimit {
		return nil, "the body is too large"
	}
	reader, err := replayable.NewReader()
	if err != nil {
		return nil, "body not replayable"
	}
	sent, err := io.ReadAll(io.LimitReader(reader, hdBodyLimit+1))
	_ = reader.Close()
	if err != nil || len(sent) > hdBodyLimit {
		return nil, "the body could not be read"
	}
	plan := &hdPlan{}
	if common.Unmarshal(sent, &plan.body) != nil || plan.body == nil {
		return nil, "the body is not a JSON object"
	}
	if common.Unmarshal(plan.body["parameters"], &plan.parameters) != nil || plan.parameters == nil {
		return nil, "the body has no parameters"
	}
	var action string
	if common.Unmarshal(plan.body["action"], &action) != nil || action != ActionGenerate {
		return nil, "not a text-to-image generation"
	}
	modelProfile, known := profiles[readString(plan.body["model"])]
	plan.model = readString(plan.body["model"])
	if !known || modelProfile.family == familyV3 || inpaintingModels[plan.model] {
		return nil, "no HD for the model " + plan.model
	}
	if common.Unmarshal(plan.body["input"], &plan.input) != nil {
		return nil, "the input is not a string"
	}
	width, widthOK := jsonInteger(plan.parameters["width"])
	height, heightOK := jsonInteger(plan.parameters["height"])
	if !widthOK || !heightOK || width < 64 || height < 64 || width%64 != 0 || height%64 != 0 || width > maxPixels || height > maxPixels {
		return nil, "the canvas is not made of multiples of 64"
	}
	plan.baseWidth, plan.baseHeight = int(width), int(height)
	if samples, ok := jsonInteger(plan.parameters["n_samples"]); !ok || samples != int64(generation.NSamples) {
		return nil, "n_samples is not the reserved count"
	}
	for _, key := range []string{"image", "mask"} {
		if raw, ok := plan.parameters[key]; ok && !isJSONNull(raw) && string(raw) != `""` {
			return nil, "the request has a source " + key
		}
	}
	if hasReferenceImages(plan.parameters) {
		return nil, "reference images"
	}
	var transparent bool
	if common.Unmarshal(plan.parameters["tag_hint_transparent_background"], &transparent) == nil && transparent {
		return nil, "a transparent background"
	}
	if plan.width, plan.height, ok = hdCanvas(plan.baseWidth, plan.baseHeight); !ok {
		return nil, "no HD canvas for " + strconv.Itoa(plan.baseWidth) + "x" + strconv.Itoa(plan.baseHeight)
	}
	plan.seed = generation.Seed
	if seed, ok := jsonInteger(plan.parameters["seed"]); ok {
		plan.seed = seed
	}
	plan.steps = generation.Steps
	if steps, ok := jsonInteger(plan.parameters["steps"]); ok {
		plan.steps = int(steps)
	}
	plan.promptAdd = modelProfile.family == familyV45 || modelProfile.family == familyV5
	plan.v5, plan.medium = modelProfile.family == familyV5, modelProfile.medium
	if modelProfile.family != familyV5 {
		plan.varietySigma = modelProfile.varietySigma
	}
	return plan, ""
}

// readString is a JSON string value, "" for anything else.
func readString(raw json.RawMessage) string {
	var value string
	_ = common.Unmarshal(raw, &value)
	return value
}

// requestBody is the enhance request for one base image: source, a raw
// base64 PNG of exactly the HD canvas, and seed, the enhance seed. Every
// key the enhance does not own goes out as sent, so the presets the base
// already carries in its prompts are not added again.
func (p *hdPlan) requestBody(source string, seed int64) ([]byte, error) {
	body, parameters := maps.Clone(p.body), maps.Clone(p.parameters)
	values := map[string]any{
		"width": p.width, "height": p.height, "image": source, "n_samples": 1,
		"strength": hdStrength, "noise": hdNoise, "seed": seed, "extra_noise_seed": seed - 1, "color_correct": false,
	}
	if raw, ok := parameters["skip_cfg_above_sigma"]; ok && !isJSONNull(raw) {
		// Variety+ grows with the square root of the canvas's area. The
		// relay's own value is computed again at the HD canvas, as the web
		// client computes it; any other number is scaled.
		var sent float64
		if common.Unmarshal(raw, &sent) == nil {
			value := sent * math.Sqrt(float64(p.width*p.height)/float64(p.baseWidth*p.baseHeight))
			if p.varietySigma > 0 && sent == p.varietySigma*math.Sqrt(float64(p.baseWidth*p.baseHeight)/varietyArea) {
				value = p.varietySigma * math.Sqrt(float64(p.width*p.height)/varietyArea)
			}
			values["skip_cfg_above_sigma"] = value
		}
	}
	for key, value := range values {
		encoded, err := common.Marshal(value)
		if err != nil {
			return nil, err
		}
		parameters[key] = encoded
	}
	for _, key := range []string{"stream", "mask", "upscaled_enhance", "upscale", "qualityToggle", "request_type", "sm", "sm_dyn"} {
		delete(parameters, key)
	}
	body["action"] = json.RawMessage(`"img2img"`)
	if p.promptAdd {
		input, err := common.Marshal(hdPrompt(p.input))
		if err != nil {
			return nil, err
		}
		body["input"] = input
		// The V4 caption mirrors input.
		var prompt, caption map[string]json.RawMessage
		if common.Unmarshal(parameters["v4_prompt"], &prompt) == nil && prompt != nil &&
			common.Unmarshal(prompt["caption"], &caption) == nil && caption != nil {
			caption["base_caption"] = input
			if prompt["caption"], err = common.Marshal(caption); err != nil {
				return nil, err
			}
			if parameters["v4_prompt"], err = common.Marshal(prompt); err != nil {
				return nil, err
			}
		}
	}
	encoded, err := common.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	body["parameters"] = encoded
	return common.Marshal(body)
}

// errBaseSize fails the enhance of a base image whose size is not the
// canvas the request asked for: a forwarder may resize, and stretching an
// unknown aspect ratio is wrong.
type errBaseSize struct{ width, height int }

func (e errBaseSize) Error() string {
	return fmt.Sprintf("the base image is %dx%d, not the canvas", e.width, e.height)
}

// enhanceSource prepares a base image NovelAI returned for its enhance, as
// NovelAI's client prepares a request image: stealth alpha 254→255 and 1→0,
// flattened onto white, resized to exactly width x height with CatmullRom
// (coverResize over the whole image), encoded as PNG base64. base is the
// size the image must have. Its memory comes from imageWork (ctx bounds the
// wait): the decode, the flattened image, coverResize's columns, the canvas
// and about two canvases of PNG encoding.
func enhanceSource(ctx context.Context, data []byte, base image.Point, width, height int) (string, error) {
	header, err := readImageHeader(data)
	if err != nil {
		return "", err
	}
	if header.Width != base.X || header.Height != base.Y {
		return "", errBaseSize{header.Width, header.Height}
	}
	w, h := int64(header.Width), int64(header.Height)
	work := min(imageWorkBudget, header.bytes+4*w*h+4*int64(width)*(h+3*int64(height)))
	if err := imageWork.Acquire(ctx, work); err != nil {
		return "", err
	}
	defer imageWork.Release(work)
	decoded, err := header.decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	bounds := decoded.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	// The colour of each pixel without premultiplication, so that raising
	// its alpha keeps it: the NRGBA PNG decodes read directly, other images
	// (WebP's) through NRGBAModel.
	nrgba, direct := decoded.(*image.NRGBA)
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			var pixel color.NRGBA
			if direct {
				i := nrgba.PixOffset(bounds.Min.X+x, bounds.Min.Y+y)
				pixel = color.NRGBA{R: nrgba.Pix[i], G: nrgba.Pix[i+1], B: nrgba.Pix[i+2], A: nrgba.Pix[i+3]}
			} else {
				pixel = color.NRGBAModel.Convert(decoded.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			}
			switch pixel.A {
			case 254:
				pixel.A = 255
			case 1:
				pixel.A = 0
			}
			// c·α + 255·(1−α), rounded.
			alpha, white := uint32(pixel.A), 255*(255-uint32(pixel.A))
			i := flat.PixOffset(x, y)
			flat.Pix[i] = uint8((uint32(pixel.R)*alpha + white + 127) / 255)
			flat.Pix[i+1] = uint8((uint32(pixel.G)*alpha + white + 127) / 255)
			flat.Pix[i+2] = uint8((uint32(pixel.B)*alpha + white + 127) / 255)
			flat.Pix[i+3] = 255
		}
	}
	return encodePNGBase64(coverResize(flat, flat.Bounds(), width, height)), nil
}

// hdRefusal is a 402 to an enhance: HD is skipped on the key until until,
// or until a balance read shows at least needed Anlas (enhanceAnlas of the
// refused call).
type hdRefusal struct {
	at, until time.Time
	needed    int
}

// reason is the refusal as a fallback reason.
func (r hdRefusal) reason() string {
	return "a 402 at " + r.at.Format(time.TimeOnly) + " marked the key out of Anlas until " + r.until.Format(time.TimeOnly)
}

// hdRefused is the refusal that skips HD on key now; an expired one is
// forgotten.
func hdRefused(key string) (hdRefusal, bool) {
	id := sha256.Sum256([]byte(key))
	value, ok := hdCannotPay.Load(id)
	if !ok {
		return hdRefusal{}, false
	}
	refusal := value.(hdRefusal)
	if !time.Now().Before(refusal.until) {
		hdCannotPay.CompareAndDelete(id, value)
		return hdRefusal{}, false
	}
	return refusal, true
}

// NoteAnlas records a NovelAI account balance read for key: enough Anlas
// for the enhance a 402 refused lift the HD cooldown that 402 set. A smaller
// balance leaves it, so the next request does not pay another 402.
func NoteAnlas(key string, anlas int64) {
	id := sha256.Sum256([]byte(key))
	if value, ok := hdCannotPay.Load(id); ok && anlas >= int64(value.(hdRefusal).needed) {
		hdCannotPay.CompareAndDelete(id, value)
	}
}

// hdOutcome is what the HD pass of a request did, for its consume log.
type hdOutcome struct {
	info                *relaycommon.RelayInfo
	canvas              string
	enhanced, delivered int
}

// enhancer runs the passes that follow a request's base pass in DoResponse,
// image by image in sample order, under the key's hold (release, once): HD
// when planned (plan), then the upscale when planned (upscale). It never
// fails the request: an image it cannot enhance is delivered as NovelAI made
// it, and HD stops for the rest of the request (stopped is why); one it
// does not upscale is delivered as it was.
type enhancer struct {
	c       *gin.Context
	info    *relaycommon.RelayInfo
	adaptor *Adaptor
	plan    *hdPlan
	upscale *upscaler
	release func()
	// upscaledBytes are the bytes of the upscaled images delivered since the
	// last holdDelivery.
	upscaledBytes int64
	// started is when the first image's preparation started, moved on by
	// the time the upscales in between took: hdBudget counts from it.
	started time.Time
	stopped string
	// cooldownUntil is the end of the cooldown a 402 of this request set.
	cooldownUntil       time.Time
	enhanced, delivered int
	clientLeft, done    bool
}

// stop ends HD for the rest of the request; the first reason stays.
func (e *enhancer) stop(reason string) {
	if e.stopped == "" {
		e.stopped = reason
	}
}

// image enhances base image sample (its bytes as NovelAI returned them). It
// returns the HD image, or nil when the base is to be delivered (HD off for
// the rest of the request, a refusal, a failure); err is set only when the
// client left.
func (e *enhancer) image(sample int, base []byte) ([]byte, error) {
	e.delivered++
	clientCtx := e.c.Request.Context()
	if err := clientCtx.Err(); err != nil {
		e.clientLeft = true
		return nil, err
	}
	if e.stopped != "" {
		return nil, nil
	}
	if e.started.IsZero() {
		e.started = time.Now()
	}
	budgetLeft := hdBudget - time.Since(e.started)
	if budgetLeft <= 0 {
		e.stop("the HD budget of " + seconds(hdBudget) + " was used up")
		return nil, nil
	}
	if refusal, refused := hdRefused(e.info.ApiKey); refused {
		e.stop(refusal.reason())
		return nil, nil
	}
	prepared, cancelPrep := context.WithTimeout(clientCtx, budgetLeft)
	source, err := enhanceSource(prepared, base, image.Pt(e.plan.baseWidth, e.plan.baseHeight), e.plan.width, e.plan.height)
	cancelPrep()
	var sizeErr errBaseSize
	switch {
	case err == nil:
	case clientCtx.Err() != nil:
		e.clientLeft = true
		return nil, clientCtx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		e.stop("the image work did not start in time")
		return nil, nil
	case errors.As(err, &sizeErr):
		e.stop(sizeErr.Error())
		return nil, nil
	default:
		e.stop("the base image could not be read: " + err.Error())
		return nil, nil
	}
	seed := hdSeed(e.plan.seed + int64(sample))
	body, err := e.plan.requestBody(source, seed)
	if err != nil {
		e.stop("the enhance request could not be built: " + err.Error())
		return nil, nil
	}
	timeout, timeoutReason := enhanceTimeout, "the enhance call timed out after "+seconds(enhanceTimeout)
	if budgetLeft = hdBudget - time.Since(e.started); budgetLeft < timeout {
		timeout, timeoutReason = budgetLeft, "the HD budget of "+seconds(hdBudget)+" was used up"
	}
	if timeout <= 0 {
		e.stop(timeoutReason)
		return nil, nil
	}
	callCtx, cancel := context.WithTimeout(clientCtx, timeout)
	defer cancel()
	logger.LogDebug(e.c, "NovelAI HD enhance of image %d: %s at %s, seed %d", sample, e.plan.model, e.plan.canvas(), seed)
	// The enhance goes to /ai/generate-image with JSON headers, whatever
	// the base pass streamed.
	e.adaptor.streamed = false
	resp, err := channel.DoApiRequestWithContext(callCtx, e.adaptor, e.c, e.info, bytes.NewReader(body))
	if err != nil {
		return e.failed(callCtx, timeoutReason, err)
	}
	defer service.CloseResponseBodyGracefully(resp)
	if isNovelAIImageHost(e.info.ChannelBaseUrl) {
		noteGenerationAnswer(e.c, e.info, resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusPaymentRequired:
		refusal := hdRefusal{at: time.Now(), needed: e.plan.anlas()}
		refusal.until = refusal.at.Add(hdCooldown)
		hdCannotPay.Store(sha256.Sum256([]byte(e.info.ApiKey)), refusal)
		e.cooldownUntil = refusal.until
		e.stop("NovelAI answered 402 (insufficient Anlas)")
		return nil, nil
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		e.stop(fmt.Sprintf("NovelAI answered HTTP %d: %s", resp.StatusCode, upstreamMessage(answer)))
		return nil, nil
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, int64(responseLimit)+1))
	if err != nil {
		return e.failed(callCtx, timeoutReason, err)
	}
	images, err := ParseImages(answer)
	if err != nil || len(images) == 0 || len(answer) > responseLimit {
		e.stop("NovelAI's answer held no image")
		return nil, nil
	}
	header, err := readImageHeader(images[0])
	if err != nil {
		e.stop("NovelAI's answer held no image")
		return nil, nil
	}
	if header.Width != e.plan.width || header.Height != e.plan.height {
		e.stop(fmt.Sprintf("NovelAI's answer is %dx%d, not the HD canvas %s", header.Width, header.Height, e.plan.canvas()))
		return nil, nil
	}
	e.enhanced++
	return images[0], nil
}

// failed settles an enhance call that broke off: the client left, the call
// ran out of time (timeoutReason), or the transport failed. It is never
// sent again: it may have reached NovelAI and spent Anlas.
func (e *enhancer) failed(callCtx context.Context, timeoutReason string, err error) ([]byte, error) {
	if clientErr := e.c.Request.Context().Err(); clientErr != nil {
		e.clientLeft = true
		return nil, clientErr
	}
	if callCtx.Err() != nil {
		e.stop(timeoutReason)
		return nil, nil
	}
	// The relay's send error hides the transport's; the diagnostic is for
	// admins.
	var transport *url.Error
	if errors.As(err, &transport) {
		err = transport.Err
	}
	e.stop("transport error: " + err.Error())
	return nil, nil
}

// upstreamMessage is NovelAI's message in an error answer
// ({"statusCode":...,"message":...}), or else the answer's text, at most
// maxErrorMessageRunes, for the logs and diagnostics.
func upstreamMessage(answer []byte) string {
	message := strings.TrimSpace(string(answer))
	var parsed struct {
		Message string `json:"message"`
	}
	if common.Unmarshal(answer, &parsed) == nil && parsed.Message != "" {
		message = parsed.Message
	}
	if runes := []rune(message); len(runes) > maxErrorMessageRunes {
		message = string(runes[:maxErrorMessageRunes])
	}
	return message
}

// reject records that the caller could not render an HD image: it counts as
// an enhance failure (HD stops), and the caller delivers the base.
func (e *enhancer) reject(sample int, cause error) {
	e.enhanced--
	e.stop("the HD image could not be rendered: " + cause.Error())
}

// deliver is base image sample as the client gets it, base as NovelAI
// returned it and final its rendering in outputFormat at quality: the HD
// image when HD made one, then that image (or the base) upscaled when the
// upscale made one, rendered alike. An image that does not render counts as
// its pass's failure, and the one before it is delivered. With last the key
// is released once NovelAI answered the image's last call, before anything
// renders, and the passes finish. err is the client's leaving. A nil
// enhancer (no pass) delivers final.
func (e *enhancer) deliver(sample int, base []byte, final renderedImage, outputFormat string, quality int, last bool) (renderedImage, error) {
	if e == nil {
		return final, nil
	}
	source := base
	var hd []byte
	if e.plan != nil {
		var err error
		if hd, err = e.image(sample, base); err != nil {
			return final, err
		}
		if hd != nil {
			source = hd
		}
	}
	var upscaled []byte
	var upscaledSize image.Point
	if e.upscale != nil {
		upscaleStarted := time.Now()
		var err error
		upscaled, upscaledSize, err = e.upscale.image(sample, source)
		if !e.started.IsZero() {
			// hdBudget counts the enhance work only.
			e.started = e.started.Add(time.Since(upscaleStarted))
		}
		if err != nil {
			e.clientLeft = true
			return final, err
		}
	}
	if last {
		e.release()
	}
	if upscaled != nil {
		rendered, format, err := renderUpscaled(e.c.Request.Context(), upscaled, outputFormat, quality)
		switch {
		case err == nil:
			e.upscaledBytes += int64(len(rendered))
			final, hd = renderedImage{rendered, format, strconv.Itoa(upscaledSize.X) + "x" + strconv.Itoa(upscaledSize.Y)}, nil
		case e.c.Request.Context().Err() != nil:
			e.clientLeft = true
			return final, e.c.Request.Context().Err()
		default:
			e.upscale.rejected(sample, err)
		}
	}
	if hd != nil {
		if rendered, format, err := RenderImage(hd, outputFormat, quality); err != nil {
			e.reject(sample, err)
		} else {
			final = renderedImage{rendered, format, e.plan.canvas()}
		}
	}
	if last {
		e.finish()
	}
	return final, nil
}

// finish releases the key's slot (once) and settles the outcomes for the
// logs: the upscale's for the consume log, and HD's success line or a
// diagnostic for admins naming why some images were delivered at the
// standard size. It is called once the last image went through deliver,
// before the relay writes it.
func (e *enhancer) finish() {
	if e.done {
		return
	}
	e.done = true
	e.release()
	if e.upscale != nil && !e.clientLeft {
		e.adaptor.upscaleResult = upscaleResult{info: e.info, counts: e.upscale.outcomes}
	}
	if e.plan == nil {
		return
	}
	e.adaptor.hdResult = hdOutcome{info: e.info, canvas: e.plan.canvas(), enhanced: e.enhanced, delivered: e.delivered}
	if e.clientLeft || e.delivered == 0 {
		e.adaptor.hdResult.delivered = 0
		return
	}
	anlas := e.plan.anlas()
	if e.enhanced == e.delivered {
		logger.LogInfo(e.c, common.LogText("NovelAI HD enhanced %d of %d images at %s, about %d Anlas", e.enhanced, e.delivered, e.plan.canvas(), e.enhanced*anlas))
		return
	}
	message := fmt.Sprintf("%s; %d of %d images enhanced at %s (about %d Anlas each)", e.stopped, e.enhanced, e.delivered, e.plan.canvas(), anlas)
	if !e.cooldownUntil.IsZero() {
		message += "; HD off for this key until " + e.cooldownUntil.Format(time.TimeOnly)
	}
	e.info.RecordConversionDiagnostics(e.c, []types.ConversionDiagnostic{{
		Code: "novelai_hd_fallback", Path: "hd", Severity: types.ConversionDiagnosticWarning,
		From: e.info.RelayFormat, To: relayconstant.RelayFormatNovelAI, Message: message,
	}})
}

// seconds is a duration in seconds, as the diagnostics name timeouts.
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}
