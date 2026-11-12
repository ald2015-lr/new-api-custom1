package novelai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/semaphore"
)

// The upscale is NovelAI's own 2x upscaler (/ai/upscale), which a NovelAI
// channel may turn on for its OpenAI-format generations
// (Generation.Upscale): once generated and enhanced (HD), each image goes to
// it in sample order, under the key's hold, and comes back at twice its
// width and height. The user's price, image count and usage do not change.
// NovelAI charges the account by the size of the image sent (upscaleAnlas).
// An upscale that fails in any way, NovelAI's 402 for an account without
// Anlas included, delivers the image from before it; only a failure the
// next image would repeat stops the request's later upscales.

const (
	upscalePath = "/ai/upscale"
	// upscaleModel is what NovelAI's web client sends whatever model drew
	// the image: the upscaler refuses the generation models' ids.
	upscaleModel = "nai-diffusion-5-curated"
	// upscaleUserAgent is a browser's: NovelAI's image host sits behind
	// Cloudflare, which may turn a client away by its User-Agent. A channel's
	// header override for User-Agent wins.
	upscaleUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// upscaleDeliveryBudget bounds the memory the responses carrying
	// upscaled images take at once to hand them to their clients.
	upscaleDeliveryBudget = 512 << 20
)

var (
	// upscaleMaxInputPixels and upscaleMaxInputSide bound the image sent, so
	// that the result stays within 4096 a side. NovelAI's other clients
	// send up to 3 MP; one source says the upscaler takes 1 MP at most. An
	// image NovelAI refuses for its size is delivered as it was, which is
	// not charged; 1_048_576 here would stop sending the images HD made.
	upscaleMaxInputPixels = 3_145_728
	upscaleMaxInputSide   = 2048
	// upscaleTimeout bounds one call, and upscaleRetryDelays are the waits
	// before sending an upscale again that NovelAI answered with 429, which
	// is not charged. Tests change them.
	upscaleTimeout     = 150 * time.Second
	upscaleRetryDelays = []time.Duration{time.Second, 2 * time.Second}
	// upscaleDelivery holds the memory of the upscaled images being handed
	// to clients: their bytes, their base64 and the JSON or event around it
	// (enhancer.holdDelivery).
	upscaleDelivery = semaphore.NewWeighted(upscaleDeliveryBudget)
)

// upscaleRequest is the body of /ai/upscale: a raw base64 PNG, whose size
// the server reads from the image.
type upscaleRequest struct {
	Image             string  `json:"image"`
	Model             string  `json:"model"`
	DeclaredBlurSigma float64 `json:"declared_blur_sigma"`
}

// upscaleAnlas is what NovelAI charges to upscale an image of pixels (its
// input), 1 to 4 Anlas, Opus included. It is not a billing quantity: the
// logs show it, and a 402 at a price stops the request's images that cost
// as much.
func upscaleAnlas(pixels int) int {
	switch {
	case pixels <= 1_048_576:
		return 1
	case pixels <= 1_747_627:
		return 2
	case pixels <= 2_446_678:
		return 3
	}
	return 4
}

// upscaleOutcome is what became of one image's upscale.
type upscaleOutcome int

const (
	upscaleApplied upscaleOutcome = iota
	upscaleNoAnlas
	upscaleNotApplicable
	upscaleFailed
	upscaleOutcomes
)

// upscaleNoteKeys are the consume log's notes of the outcomes, in order.
var upscaleNoteKeys = [upscaleOutcomes]string{
	"Upscaled 2x: {{count}}",
	"Not upscaled, not enough Anlas: {{count}}",
	"Not upscaled, not applicable: {{count}}",
	"Upscale failed, image before it kept: {{count}}",
}

// upscaleResult is what the upscale did for the images a request delivered,
// for its consume log.
type upscaleResult struct {
	info   *relaycommon.RelayInfo
	counts [upscaleOutcomes]int
}

// notes are the consume log's notes, one per outcome some image had;
// nothing once the client left.
func (r upscaleResult) notes() []*common.Message {
	if r.info == nil || clientLeftRequest(r.info) {
		return nil
	}
	var notes []*common.Message
	for outcome, count := range r.counts {
		if count > 0 {
			notes = append(notes, common.NewMessage(upscaleNoteKeys[outcome], map[string]any{"count": count}))
		}
	}
	return notes
}

// upscaler upscales one request's images, called by enhancer.deliver after
// HD, under the key's hold. It never fails the request: an image it does not
// upscale is delivered as it was.
type upscaler struct {
	c    *gin.Context
	info *relaycommon.RelayInfo
	// total is the request's image count, for the logs.
	total int
	// refusedAt is the price of the upscale NovelAI refused with 402, 0
	// before any: the later images that cost as much are not sent. stopped
	// is why no later image is sent after a failure it would repeat.
	refusedAt int
	stopped   string
	outcomes  [upscaleOutcomes]int
}

// settle counts image sample's outcome and logs why: a failed upscale as a
// warning, one not sent as information.
func (u *upscaler) settle(sample int, outcome upscaleOutcome, reason string) {
	u.outcomes[outcome]++
	if outcome == upscaleFailed {
		logger.LogWarn(u.c, common.LogText("NovelAI upscale of image %d of %d failed, returning the image before it: %s", sample+1, u.total, reason))
		return
	}
	logger.LogInfo(u.c, common.LogText("NovelAI upscale of image %d of %d skipped: %s", sample+1, u.total, reason))
}

// rejected turns the applied upscale of image sample, which the relay could
// not render, into a failure: the image before it is delivered.
func (u *upscaler) rejected(sample int, cause error) {
	u.outcomes[upscaleApplied]--
	u.settle(sample, upscaleFailed, "the upscaled image could not be rendered: "+cause.Error())
}

// image upscales data, image sample as NovelAI returned it (the HD image or
// the base). It returns the upscaled image and its size, or nil when the
// image before it is to be delivered, its outcome settled; err is set only
// when the client left. Only a PNG within upscaleMaxInputPixels and
// upscaleMaxInputSide is sent, and the answer counts only as an image of
// twice its size (NovelAI may first round each side down to a multiple of
// 16), read from its header and never decoded.
func (u *upscaler) image(sample int, data []byte) ([]byte, image.Point, error) {
	clientCtx := u.c.Request.Context()
	if err := clientCtx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if format := SniffImageFormat(data); format != formatPNG {
		u.settle(sample, upscaleNotApplicable, "the image is "+cmp.Or(format, "of an unknown format")+", the upscaler takes PNG")
		return nil, image.Point{}, nil
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		u.settle(sample, upscaleFailed, "unreadable PNG header: "+err.Error())
		return nil, image.Point{}, nil
	}
	width, height := config.Width, config.Height
	switch {
	case max(width, height) > upscaleMaxInputSide:
		u.settle(sample, upscaleNotApplicable, fmt.Sprintf("the image is %dx%d, over %d px a side", width, height, upscaleMaxInputSide))
		return nil, image.Point{}, nil
	case width*height > upscaleMaxInputPixels:
		u.settle(sample, upscaleNotApplicable, fmt.Sprintf("the image is %dx%d, over %d pixels", width, height, upscaleMaxInputPixels))
		return nil, image.Point{}, nil
	case u.stopped != "":
		u.settle(sample, upscaleFailed, u.stopped)
		return nil, image.Point{}, nil
	}
	price := upscaleAnlas(width * height)
	if u.refusedAt > 0 && price >= u.refusedAt {
		u.settle(sample, upscaleNoAnlas, fmt.Sprintf("NovelAI refused an upscale of %d Anlas with 402", u.refusedAt))
		return nil, image.Point{}, nil
	}
	body, err := common.Marshal(upscaleRequest{Image: base64.StdEncoding.EncodeToString(data), Model: upscaleModel})
	if err != nil {
		u.settle(sample, upscaleFailed, "the request could not be built: "+err.Error())
		return nil, image.Point{}, nil
	}

	started := time.Now()
	answer, err := u.call(sample, body)
	if err != nil {
		if clientErr := clientCtx.Err(); clientErr != nil {
			return nil, image.Point{}, clientErr
		}
		// NovelAI may still be busy with it, or be down: the next image
		// would wait as long.
		u.stopped = "an earlier upscale failed: " + err.Error()
		u.settle(sample, upscaleFailed, err.Error())
		return nil, image.Point{}, nil
	}
	reason := fmt.Sprintf("HTTP %d: %s", answer.status, upstreamMessage(answer.body))
	switch status := answer.status; {
	case status == http.StatusOK || status == http.StatusCreated:
	case status == http.StatusPaymentRequired:
		u.refusedAt = price
		u.settle(sample, upscaleNoAnlas, reason)
		return nil, image.Point{}, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status >= http.StatusInternalServerError:
		// The key or the endpoint is unusable, or NovelAI unhealthy.
		u.stopped = "an earlier upscale answered " + reason
		u.settle(sample, upscaleFailed, reason)
		return nil, image.Point{}, nil
	default:
		// A refusal of this image (400, 413, 422, a 429 that lasted) or a
		// redirect, which is not followed.
		u.settle(sample, upscaleFailed, reason)
		return nil, image.Point{}, nil
	}
	if answer.tooLarge {
		u.settle(sample, upscaleFailed, fmt.Sprintf("the answer exceeds %d bytes", responseLimit))
		return nil, image.Point{}, nil
	}
	images, err := ParseImages(answer.body)
	if err != nil || len(images) == 0 {
		u.settle(sample, upscaleFailed, "the answer held no image")
		return nil, image.Point{}, nil
	}
	if len(images) > 1 {
		logger.LogDebug(u.c, "NovelAI upscale of image %d answered %d images, keeping the first", sample+1, len(images))
	}
	upscaled, _, err := image.DecodeConfig(bytes.NewReader(images[0]))
	if err != nil {
		u.settle(sample, upscaleFailed, "the answer's image is unreadable: "+err.Error())
		return nil, image.Point{}, nil
	}
	if !twice(width, upscaled.Width) || !twice(height, upscaled.Height) {
		u.settle(sample, upscaleFailed, fmt.Sprintf("the answer is %dx%d, not twice %dx%d", upscaled.Width, upscaled.Height, width, height))
		return nil, image.Point{}, nil
	}
	u.outcomes[upscaleApplied]++
	logger.LogInfo(u.c, common.LogText("NovelAI upscale of image %d of %d: %dx%d to %dx%d in %s, about %d Anlas", sample+1, u.total, width, height, upscaled.Width, upscaled.Height, time.Since(started).Round(time.Millisecond), price))
	return images[0], image.Pt(upscaled.Width, upscaled.Height), nil
}

// twice tells that side is the upscaler's 2x of input: twice input, or of
// input rounded down to a multiple of 16.
func twice(input, side int) bool {
	return 2*(input&^15) <= side && side <= 2*input
}

// upscaleAnswer is NovelAI's answer to an upscale: its status and its body,
// read within responseLimit for a success (tooLarge past it, without the
// body) and errorBodyLimit otherwise.
type upscaleAnswer struct {
	status   int
	body     []byte
	tooLarge bool
}

// call sends body to NovelAI's upscaler, and again after a 429 while
// upscaleRetryDelays last. err is a call that failed or ran out of time,
// which is never sent again (it may have been charged), or the client's
// leaving.
func (u *upscaler) call(sample int, body []byte) (upscaleAnswer, error) {
	clientCtx := u.c.Request.Context()
	for attempt := 0; ; attempt++ {
		answer, err := u.send(body)
		if err != nil || answer.status != http.StatusTooManyRequests || attempt == len(upscaleRetryDelays) {
			return answer, err
		}
		delay := upscaleRetryDelays[attempt]
		logger.LogInfo(u.c, common.LogText("NovelAI upscale of image %d of %d answered 429, retrying in %s", sample+1, u.total, delay))
		timer := time.NewTimer(delay)
		select {
		case <-clientCtx.Done():
			timer.Stop()
			return upscaleAnswer{}, clientCtx.Err()
		case <-timer.C:
		}
	}
}

// send makes one upscale call to the channel's base URL, as the generation
// goes: the key as a Bearer token, then the channel's header override, and
// its proxy. A redirect is returned, not followed, so the key never reaches
// another host. NovelAI's answer is noted for the key on its own host, as a
// generation's is.
func (u *upscaler) send(body []byte) (upscaleAnswer, error) {
	ctx, cancel := context.WithTimeout(u.c.Request.Context(), upscaleTimeout)
	defer cancel()
	failed := func(err error) (upscaleAnswer, error) {
		if ctx.Err() != nil && u.c.Request.Context().Err() == nil {
			return upscaleAnswer{}, errors.New("timed out after " + upscaleTimeout.String())
		}
		// A transport error repeats the URL, which carries no key.
		var transport *url.Error
		if errors.As(err, &transport) {
			err = transport.Err
		}
		return upscaleAnswer{}, fmt.Errorf("transport error: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, imageBaseURL(u.info.ChannelBaseUrl)+upscalePath, bytes.NewReader(body))
	if err != nil {
		return failed(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", upscaleUserAgent)
	request.Header.Set("Authorization", "Bearer "+u.info.ApiKey)
	if err := channel.ApplyHeaderOverride(u.info, u.c, request); err != nil {
		return upscaleAnswer{}, fmt.Errorf("the header override failed: %w", err)
	}
	client, err := service.GetHttpClientWithProxySettings(u.info.ChannelSetting.Proxy, u.info.ChannelSetting)
	if err != nil {
		return upscaleAnswer{}, fmt.Errorf("no HTTP client for the channel's proxy: %w", err)
	}
	// The cached client is shared, so the redirect policy goes on a copy.
	upscaleClient := *client
	upscaleClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := upscaleClient.Do(request)
	if err != nil {
		return failed(err)
	}
	defer service.CloseResponseBodyGracefully(resp)
	if isNovelAIImageHost(u.info.ChannelBaseUrl) {
		noteGenerationAnswer(u.c, u.info, resp.StatusCode)
	}
	answer := upscaleAnswer{status: resp.StatusCode}
	succeeded := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated
	limit := int64(errorBodyLimit)
	if succeeded {
		limit = int64(responseLimit) + 1
	}
	if answer.body, err = io.ReadAll(io.LimitReader(resp.Body, limit)); err != nil {
		return failed(err)
	}
	if succeeded && len(answer.body) > responseLimit {
		answer.body, answer.tooLarge = nil, true
	}
	return answer, nil
}

// renderUpscaled is an upscaled image in outputFormat at quality, as
// RenderImage makes it. An image already in that format, the PNG the
// upscaler answers for PNG output and chat, goes out as NovelAI sent it and
// is never decoded. Any other is decoded within readImageHeader's limits once
// the memory the decode and its flattened copy need of imageWork is free;
// ctx (the client's) bounds the wait.
func renderUpscaled(ctx context.Context, data []byte, outputFormat string, quality int) ([]byte, string, error) {
	format := SniffImageFormat(data)
	if outputFormat == "" || outputFormat == format || outputFormat == formatWebP {
		return RenderImage(data, outputFormat, quality)
	}
	header, err := readImageHeader(data)
	if err != nil {
		return nil, "", err
	}
	work := min(imageWorkBudget, header.bytes+4*int64(header.Width)*int64(header.Height))
	if err := imageWork.Acquire(ctx, work); err != nil {
		return nil, "", err
	}
	defer imageWork.Release(work)
	return RenderImage(data, outputFormat, quality)
}

// holdDelivery takes the memory a response needs to hand its client the
// upscaled images rendered since the last hold: four times their bytes, for
// the bytes, their base64 and the JSON or event around them, at most the
// whole budget. It returns the release, a no-op when there are none. Only a
// request that no longer holds its key waits (wait); one that does goes on
// without the hold when the memory is taken, so that a key never waits for
// it. err is the client's leaving during the wait.
func (e *enhancer) holdDelivery(wait bool) (func(), error) {
	if e == nil || e.upscaledBytes == 0 {
		return func() {}, nil
	}
	weight := min(upscaleDeliveryBudget, 4*e.upscaledBytes)
	e.upscaledBytes = 0
	switch {
	case !wait:
		if !upscaleDelivery.TryAcquire(weight) {
			return func() {}, nil
		}
	default:
		if err := upscaleDelivery.Acquire(e.c.Request.Context(), weight); err != nil {
			return nil, err
		}
	}
	return sync.OnceFunc(func() { upscaleDelivery.Release(weight) }), nil
}
