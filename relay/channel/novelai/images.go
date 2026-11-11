package novelai

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// maxInputPixels bounds a decoded source image or mask (about 8192x4096):
// a decoded image takes 4 bytes a pixel.
const maxInputPixels = 1 << 25

// Image formats as OpenAI names them in output_format.
const (
	formatPNG  = "png"
	formatJPEG = "jpeg"
	formatWebP = "webp"
)

// responseLimit bounds a NovelAI response held to convert it, and what a zip
// may expand to.
var responseLimit = 64 << 20

// SniffImageFormat names the format of image bytes from their magic:
// "png", "jpeg", "webp", or "" for anything else.
func SniffImageFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return formatJPEG
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP":
		return formatWebP
	}
	return ""
}

// decodeImage decodes a PNG, JPEG or WebP image of at most maxInputPixels.
func decodeImage(data []byte) (image.Image, error) {
	format := SniffImageFormat(data)
	var decodeConfig func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch format {
	case formatPNG:
		decodeConfig, decode = png.DecodeConfig, png.Decode
	case formatJPEG:
		decodeConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	case formatWebP:
		decodeConfig, decode = webp.DecodeConfig, webp.Decode
	default:
		return nil, errors.New("not a PNG, JPEG or WebP image")
	}
	config, err := decodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxInputPixels {
		return nil, fmt.Errorf("the image is %dx%d", config.Width, config.Height)
	}
	return decode(bytes.NewReader(data))
}

func decodeImageInput(c *gin.Context, data []byte, field string) (image.Image, *types.NewAPIError) {
	decoded, err := decodeImage(data)
	if err != nil {
		return nil, badRequest(c, i18n.MsgNovelAIImageInvalid, map[string]any{"Field": field, "Max": maxInputPixels})
	}
	return decoded, nil
}

// coverResize scales src to exactly width x height: it keeps the centered
// part of src with the target's aspect ratio (as CSS object-fit: cover) and
// resamples it with scaler. NovelAI requires an edit's image, and its mask,
// to match the canvas.
func coverResize(src image.Image, width, height int, scaler draw.Scaler) *image.RGBA {
	bounds := src.Bounds()
	crop := bounds
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth*height > width*sourceHeight {
		cropWidth := max(1, sourceHeight*width/height)
		crop.Min.X += (sourceWidth - cropWidth) / 2
		crop.Max.X = crop.Min.X + cropWidth
	} else {
		cropHeight := max(1, sourceWidth*height/width)
		crop.Min.Y += (sourceHeight - cropHeight) / 2
		crop.Max.Y = crop.Min.Y + cropHeight
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	scaler.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	return dst
}

func encodePNG(img image.Image) []byte {
	var buffer bytes.Buffer
	// Encoding an in-memory image cannot fail.
	_ = png.Encode(&buffer, img)
	return buffer.Bytes()
}

func encodePNGBase64(img image.Image) string {
	return base64.StdEncoding.EncodeToString(encodePNG(img))
}

// novelAIMask converts a mask to NovelAI's: an opaque PNG at the canvas size,
// white where NovelAI repaints. OpenAI marks the area to edit with fully
// transparent pixels; a mask without any is read as a black-and-white
// NovelAI mask, white (brighter than mid-grey) marking the area. The mask is
// cropped like the image and scaled with nearest neighbour, then snapped to
// the 8x8 blocks of NovelAI's latent grid: a block is white when more than
// half of it is. It reports false when no block is white.
func novelAIMask(mask image.Image, width, height int) (string, bool) {
	bounds := mask.Bounds()
	transparent := false
	for y := bounds.Min.Y; y < bounds.Max.Y && !transparent; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := mask.At(x, y).RGBA(); alpha == 0 {
				transparent = true
				break
			}
		}
	}
	binary := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := mask.At(x, y)
			repaint := false
			if transparent {
				_, _, _, alpha := pixel.RGBA()
				repaint = alpha == 0
			} else {
				repaint = color.GrayModel.Convert(pixel).(color.Gray).Y > 127
			}
			if repaint {
				binary.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	scaled := coverResize(binary, width, height, draw.NearestNeighbor)
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	selected := false
	for blockY := 0; blockY < height; blockY += 8 {
		for blockX := 0; blockX < width; blockX += 8 {
			white, total := 0, 0
			for y := blockY; y < min(blockY+8, height); y++ {
				for x := blockX; x < min(blockX+8, width); x++ {
					total++
					if scaled.RGBAAt(x, y).R > 127 {
						white++
					}
				}
			}
			value := uint8(0)
			if 2*white > total {
				value, selected = 255, true
			}
			for y := blockY; y < min(blockY+8, height); y++ {
				for x := blockX; x < min(blockX+8, width); x++ {
					out.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
				}
			}
		}
	}
	if !selected {
		return "", false
	}
	return encodePNGBase64(out), true
}

// LoadImageReference reads an image an OpenAI request refers to: a data URL,
// raw base64, or an http(s) URL downloaded through new-api's SSRF-protected
// fetcher and its size limit. field names the request field in errors.
func LoadImageReference(c *gin.Context, reference, field string) ([]byte, *types.NewAPIError) {
	reference = strings.TrimSpace(reference)
	invalid := badRequest(c, i18n.MsgNovelAIImageInvalid, map[string]any{"Field": field, "Max": maxInputPixels})
	lower := strings.ToLower(reference)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		encoded, _, err := service.GetBase64Data(c, types.NewURLFileSource(reference), "novelai "+field)
		if err != nil {
			return nil, badRequest(c, i18n.MsgNovelAIImageFetchFailed, map[string]any{"Field": field, "Error": err.Error()})
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, invalid
		}
		return data, nil
	}
	if strings.HasPrefix(lower, "data:") {
		header, payload, ok := strings.Cut(reference, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
			return nil, invalid
		}
		reference = payload
	}
	data, err := base64.StdEncoding.DecodeString(reference)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(reference); err != nil {
			return nil, invalid
		}
	}
	if len(data) == 0 {
		return nil, invalid
	}
	return data, nil
}

// ParseImages reads the images of a complete /ai/generate-image response:
// the image entries of a zip (NovelAI's default, image_0.png, ... in archive
// order; .png, .webp, .jpg or any entry with image magic), the images of a
// JSON {"images":[{"image":"<base64>"}, ...]} answer, or one bare PNG, WebP or
// JPEG. Only image payloads count, never the length of a list. A zip is read
// through responseLimit in all, whatever sizes it claims.
func ParseImages(body []byte) ([][]byte, error) {
	if SniffImageFormat(body) != "" {
		return [][]byte{body}, nil
	}
	if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		var images [][]byte
		budget := &io.LimitedReader{N: int64(responseLimit) + 1}
		for _, file := range archive.File {
			if file.FileInfo().IsDir() {
				continue
			}
			if len(images) == dto.MaxImageN {
				return nil, fmt.Errorf("more than %d images", dto.MaxImageN)
			}
			entry, err := file.Open()
			if err != nil {
				return nil, err
			}
			budget.R = entry
			data, err := io.ReadAll(budget)
			_ = entry.Close()
			if err != nil {
				return nil, err
			}
			if budget.N == 0 {
				return nil, errors.New("the archive expands beyond the response limit")
			}
			if SniffImageFormat(data) != "" {
				images = append(images, data)
			}
		}
		return images, nil
	}
	if !ojson.ValidBytes(body) {
		return nil, errors.New("neither a zip, an image nor JSON")
	}
	var images [][]byte
	var decodeErr error
	collect := func(_, item ojson.Result) bool {
		value := item.Get("image")
		if value.Type != ojson.String || value.Str == "" {
			return true
		}
		if len(images) == dto.MaxImageN {
			decodeErr = fmt.Errorf("more than %d images", dto.MaxImageN)
			return false
		}
		data, err := base64.StdEncoding.DecodeString(value.Str)
		if err != nil || SniffImageFormat(data) == "" {
			decodeErr = fmt.Errorf("image %d is not base64 image data", len(images))
			return false
		}
		images = append(images, data)
		return true
	}
	list := ojson.GetBytes(body, "images")
	if list.IsObject() {
		collect(ojson.Result{}, list)
	} else if list.IsArray() {
		list.ForEach(collect)
	}
	return images, decodeErr
}

// RenderImage turns an image NovelAI returned into the bytes and the format
// a client asked for: jpeg is transcoded (alpha flattened onto white, at
// quality, 1 to 100), and png when NovelAI answered in another format;
// webp keeps what NovelAI sent, as it encodes WebP itself. The format
// returned is the one of the bytes.
func RenderImage(data []byte, outputFormat string, quality int) ([]byte, string, error) {
	format := SniffImageFormat(data)
	if format == "" {
		return nil, "", errors.New("not a PNG, JPEG or WebP image")
	}
	if outputFormat == "" || outputFormat == format || outputFormat == formatWebP {
		return data, format, nil
	}
	decoded, err := decodeImage(data)
	if err != nil {
		return nil, "", err
	}
	if outputFormat == formatPNG {
		return encodePNG(decoded), formatPNG, nil
	}
	flattened := image.NewRGBA(decoded.Bounds())
	draw.Draw(flattened, flattened.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(flattened, flattened.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, flattened, &jpeg.Options{Quality: min(max(quality, 1), 100)}); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), formatJPEG, nil
}

// DataURL is an image as a data: URL.
func DataURL(data []byte, format string) string {
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data)
}
