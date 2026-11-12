package novelai

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// generateImageStreamPath is NovelAI's streaming generation endpoint, which
// answers with previews and the final images as server-sent events.
const generateImageStreamPath = "/ai/generate-image-stream"

// novelAIStreamEvent is the data of one /ai/generate-image-stream event in
// SSE mode; the event: line repeats event_type. intermediate previews sample
// samp_ix after step step_ix, final is a finished image, error ends the
// stream, even after HTTP 200.
type novelAIStreamEvent struct {
	EventType   string `json:"event_type"`
	SampleIndex *int   `json:"samp_ix"`
	StepIndex   int    `json:"step_ix"`
	Image       string `json:"image"`
	Message     string `json:"message"`
}

// openAIImageStreamEvent is an OpenAI image stream event:
// image_generation.partial_image / image_generation.completed, or the
// image_edit ones. seed is an extension, as in the non-stream response.
type openAIImageStreamEvent struct {
	Type              string            `json:"type"`
	PartialImageIndex *int              `json:"partial_image_index,omitempty"`
	CreatedAt         int64             `json:"created_at"`
	Size              string            `json:"size"`
	Quality           string            `json:"quality"`
	Background        string            `json:"background"`
	OutputFormat      string            `json:"output_format"`
	Seed              *int64            `json:"seed,omitempty"`
	Usage             *OpenAIImageUsage `json:"usage,omitempty"`
	B64JSON           string            `json:"b64_json"`
}

// renderedImage is a final image in the format the client asked for.
type renderedImage struct {
	data   []byte
	format string
}

// imageStream answers a stream: true OpenAI images request with OpenAI's
// image stream events, from NovelAI's stream (V4 and later) or from its
// complete answer (V3, which cannot stream).
//
// Partial images come from sample 0 only (OpenAI's events name no image):
// at most output.PartialImages previews, evenly spaced over the steps. Each
// final becomes one completed event, in sample order; the last one carries
// the usage of the images delivered with it and the others zeros, so summing
// them counts the prompt once. No [DONE] follows, as OpenAI sends none.
//
// The billed quantity is the completed events delivered once upstream ended
// the stream; a client that left keeps the reservation, as does a stream
// that broke (a timeout or a transport error) before NovelAI finished it.
// The usage returned is the generation's for the billed quantity.
type imageStream struct {
	c          *gin.Context
	info       *relaycommon.RelayInfo
	generation *Generation
	output     ImageOutput
	created    int64
	partials   int
	// received marks the samples whose final arrived; pending holds those
	// waiting for an earlier sample, next is the sample to send next.
	received  []bool
	pending   map[int]renderedImage
	next      int
	delivered int
	// clientGone is the failure of a write the client did not take.
	clientGone error
	// failure is NovelAI's own end of the stream short of its images: an
	// error event, or an event that breaks the protocol.
	failure error
}

// imagesStreamResponse answers a stream: true OpenAI images request.
// NovelAI's event stream is relayed as it arrives; any other 2xx body (a
// V3 model's zip, or a forwarder that does not stream) is read whole and
// sent as completed events.
func imagesStreamResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, generation *Generation, output ImageOutput) (*dto.Usage, *types.NewAPIError) {
	stream := &imageStream{
		c: c, info: info, generation: generation, output: output,
		created:  time.Now().Unix(),
		received: make([]bool, generation.NSamples),
		pending:  map[int]renderedImage{},
	}
	eventStream := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if !eventStream {
		// A forwarder may label its event stream otherwise.
		reader := bufio.NewReader(resp.Body)
		head, _ := reader.Peek(6)
		eventStream = bytes.HasPrefix(head, []byte("event:")) || bytes.HasPrefix(head, []byte("data:")) || bytes.HasPrefix(head, []byte(":"))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{reader, resp.Body}
	}
	if !eventStream {
		return stream.synthesize(resp)
	}
	helper.StreamScannerHandler(c, resp, info, stream.handle)
	return stream.finish()
}

// partialStep is the number of steps after which partial image k (0-based)
// previews the generation: the steps split evenly into PartialImages + 1.
func (s *imageStream) partialStep(k int) int {
	parts := s.output.PartialImages + 1
	return max((s.generation.Steps*(k+1)+parts-1)/parts, 1)
}

// handle reads one NovelAI stream event.
func (s *imageStream) handle(data string, sr *helper.StreamResult) {
	var event novelAIStreamEvent
	if err := common.UnmarshalJsonStr(data, &event); err != nil {
		s.fail(sr, fmt.Errorf("an event that is not JSON: %w", err))
		return
	}
	switch event.EventType {
	case "intermediate":
		if s.partials >= s.output.PartialImages || event.SampleIndex == nil || *event.SampleIndex != 0 || event.StepIndex+1 < s.partialStep(s.partials) {
			return
		}
		preview, err := base64.StdEncoding.DecodeString(event.Image)
		format := SniffImageFormat(preview)
		if err != nil || format == "" {
			// A preview is optional; a broken one is skipped.
			return
		}
		size := s.generation.Size()
		if config, _, err := image.DecodeConfig(bytes.NewReader(preview)); err == nil {
			size = strconv.Itoa(config.Width) + "x" + strconv.Itoa(config.Height)
		}
		index := s.partials
		if err := s.write(openAIImageStreamEvent{
			Type: s.output.Event + ".partial_image", PartialImageIndex: &index, CreatedAt: s.created, Size: size,
			Quality: s.output.Quality, Background: s.output.Background, OutputFormat: format, B64JSON: event.Image,
		}); err != nil {
			s.clientGone = err
			sr.Stop(err)
			return
		}
		s.partials++
	case "final":
		if event.SampleIndex == nil || *event.SampleIndex < 0 || *event.SampleIndex >= s.generation.NSamples || s.received[*event.SampleIndex] {
			s.fail(sr, errors.New("a final event for a sample that was not asked for or already finished"))
			return
		}
		data, err := base64.StdEncoding.DecodeString(event.Image)
		if err != nil {
			s.fail(sr, fmt.Errorf("a final image that is not base64: %w", err))
			return
		}
		rendered, format, err := RenderImage(data, s.output.OutputFormat, s.output.Compression)
		if err != nil {
			s.fail(sr, fmt.Errorf("a final event without an image: %w", err))
			return
		}
		s.received[*event.SampleIndex] = true
		s.pending[*event.SampleIndex] = renderedImage{rendered, format}
		for {
			final, ok := s.pending[s.next]
			if !ok {
				break
			}
			if err := s.complete(s.next, final, s.delivered+1 == s.generation.NSamples); err != nil {
				s.clientGone = err
				sr.Stop(err)
				return
			}
			delete(s.pending, s.next)
			s.next++
		}
		if s.delivered == s.generation.NSamples {
			sr.Done()
		}
	case "error":
		message := strings.TrimSpace(event.Message)
		if message == "" {
			message = "error event"
		}
		s.fail(sr, errors.New(message))
	}
	// Other events (retry, or ones NovelAI adds) carry no image.
}

// fail ends the stream on NovelAI's side.
func (s *imageStream) fail(sr *helper.StreamResult, err error) {
	s.failure = err
	sr.Stop(err)
}

// complete sends the completed event of sample's image; the last one
// carries the usage of the images delivered with it.
func (s *imageStream) complete(sample int, final renderedImage, last bool) error {
	seed := s.generation.Seed + int64(sample)
	usage := OpenAIImageUsage{}
	if last {
		usage = ImageUsage(s.generation.Tokens.Usage(s.delivered + 1))
	}
	if err := s.write(openAIImageStreamEvent{
		Type: s.output.Event + ".completed", CreatedAt: s.created, Size: s.generation.Size(), Quality: s.output.Quality,
		Background: s.output.Background, OutputFormat: final.format, Seed: &seed, Usage: &usage,
		B64JSON: base64.StdEncoding.EncodeToString(final.data),
	}); err != nil {
		return err
	}
	s.delivered++
	return nil
}

func (s *imageStream) write(event openAIImageStreamEvent) error {
	payload, err := common.Marshal(event)
	if err != nil {
		return err
	}
	return helper.ResponseChunkData(s.c, dto.ResponsesStreamResponse{Type: event.Type}, string(payload))
}

// usage is the generation's usage for the quantity the request settles at.
func (s *imageStream) usage() *dto.Usage {
	return s.generation.Tokens.Usage(s.info.SettledImageCount())
}

// finish settles a relayed stream once it ended.
func (s *imageStream) finish() (*dto.Usage, *types.NewAPIError) {
	status := s.info.StreamStatus
	if s.delivered == s.generation.NSamples {
		s.info.UpdateImageCount(int64(s.delivered))
		return s.usage(), nil
	}
	if s.clientGone == nil {
		// The images that came before a missing one still reach the client.
		samples := make([]int, 0, len(s.pending))
		for sample := range s.pending {
			samples = append(samples, sample)
		}
		slices.Sort(samples)
		for i, sample := range samples {
			if err := s.complete(sample, s.pending[sample], i == len(samples)-1); err != nil {
				s.clientGone = err
				break
			}
		}
	}
	if s.clientGone != nil {
		// Nothing more is written to it. NovelAI may have ended its stream
		// before the write failed, which the end reason then says; the
		// stream goroutines are done, so the client that left replaces it,
		// and perf metrics take the request for a cancellation.
		status.EndReason, status.EndError = relaycommon.StreamEndReasonClientGone, s.clientGone
		logger.LogWarn(s.c, common.LogText("NovelAI client left the stream after %d of %d images, billing the reserved %d images", s.delivered, s.generation.NSamples, s.info.RequestedImageCount()))
		return s.usage(), nil
	}
	upstreamEnded := s.failure != nil || status.EndReason == relaycommon.StreamEndReasonEOF || status.EndReason == relaycommon.StreamEndReasonDone
	if !upstreamEnded {
		// The client left while nothing was written, or the stream broke (a
		// timeout, a transport error): NovelAI may still be generating what
		// it charged for, so only a finished stream lowers the count.
		helper.ReportStreamInterruption(s.c, s.info)
		return s.usage(), nil
	}
	message := i18n.T(s.c, i18n.MsgNovelAIStreamIncomplete, map[string]any{"Count": s.delivered, "Total": s.generation.NSamples})
	if s.failure != nil {
		message = i18n.T(s.c, i18n.MsgNovelAIStreamFailed, map[string]any{"Error": s.failure.Error()})
	}
	apiErr := types.NewOpenAIError(errors.New(message), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	if s.delivered == 0 {
		// Nothing to bill: the request fails, and the error reaches the
		// client as a stream event.
		return nil, apiErr
	}
	s.info.UpdateImageCount(int64(s.delivered))
	status.MarkFailed(string(apiErr.GetErrorCode()), string(apiErr.GetErrorType()), apiErr.StatusCode)
	common.SetContextKey(s.c, constant.ContextKeyResponseStreamStatus, status)
	helper.WriteStreamError(s.c, s.info.RelayFormat, apiErr)
	return s.usage(), nil
}

// synthesize answers from a complete NovelAI response (zip, JSON or a bare
// image): one completed event per image, the usage on the last. The images
// all arrive at once, after NovelAI made them, so the stream's throughput
// spans the whole request (PerformanceWholeOutput).
func (s *imageStream) synthesize(resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	s.info.PerformanceWholeOutput = true
	images, clientLeft, apiErr := readImages(s.c, resp, s.info, s.generation.NSamples)
	if clientLeft {
		return s.usage(), nil
	}
	if apiErr != nil {
		return nil, apiErr
	}
	finals := make([]renderedImage, 0, len(images))
	for _, data := range images {
		rendered, format, err := RenderImage(data, s.output.OutputFormat, s.output.Compression)
		if err != nil {
			return nil, noImageError(s.c, err)
		}
		finals = append(finals, renderedImage{rendered, format})
	}
	s.info.StreamStatus = relaycommon.NewStreamStatus()
	s.info.SetFirstResponseTime()
	helper.SetEventStreamHeaders(s.c)
	s.c.Status(http.StatusOK)
	for i, final := range finals {
		if err := s.complete(i, final, i == len(finals)-1); err != nil {
			s.info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			logger.LogWarn(s.c, common.LogText("NovelAI client left the stream after %d of %d images, billing the reserved %d images", s.delivered, len(finals), s.info.RequestedImageCount()))
			return s.usage(), nil
		}
	}
	s.info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	s.info.UpdateImageCount(int64(s.delivered))
	return s.usage(), nil
}
