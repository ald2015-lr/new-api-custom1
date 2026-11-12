package novelai

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
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
	"golang.org/x/sync/semaphore"
)

// An image or mask a request sends may have at most maxInputPixels pixels
// (4096x4096) and maxInputSide a side, and decoding it may take at most
// maxDecodeBytes besides a copy of its bytes (decodeBytesPerPixel):
// 4096x4096 at 4 bytes a pixel.
const (
	maxInputPixels = 1 << 24
	maxInputSide   = 65_535
	maxDecodeBytes = 64 << 20
)

// imageWork bounds the memory the image work of all requests (decoding an
// edit's image and mask, resizing them to the canvas) takes at once: a
// request's work runs once it holds about the bytes it needs of
// imageWorkBudget, or ends when its client leaves (BuildGeneration). One
// request needs at most about 190 MiB: two images of maxDecodeBytes, the
// resize's columns and three canvases.
var imageWork = semaphore.NewWeighted(imageWorkBudget)

const imageWorkBudget = 256 << 20

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

// imageHeader is what the header of an image tells before anything decodes
// it.
type imageHeader struct {
	image.Config
	// bytes is about the most memory decoding the image takes, and maxPixels
	// the most pixels an image of its kind may have (decodeBytesPerPixel).
	bytes     int64
	maxPixels int
	decode    func(io.Reader) (image.Image, error)
}

// readImageHeader reads the header of a PNG, JPEG or WebP image; it fails
// for an image past maxInputSide, or past the pixels of its kind.
func readImageHeader(data []byte) (imageHeader, error) {
	header := imageHeader{maxPixels: maxInputPixels}
	var decodeConfig func(io.Reader) (image.Config, error)
	format := SniffImageFormat(data)
	switch format {
	case formatPNG:
		decodeConfig, header.decode = png.DecodeConfig, png.Decode
	case formatJPEG:
		decodeConfig, header.decode = jpeg.DecodeConfig, jpeg.Decode
	case formatWebP:
		decodeConfig, header.decode = webp.DecodeConfig, webp.Decode
	default:
		return header, errors.New("not a PNG, JPEG or WebP image")
	}
	config, err := decodeConfig(bytes.NewReader(data))
	if err != nil {
		return header, err
	}
	header.Config = config
	perPixel := decodeBytesPerPixel(data, format, config.ColorModel)
	header.maxPixels = min(maxInputPixels, int(maxDecodeBytes/perPixel))
	header.bytes = int64(perPixel * float64(config.Width) * float64(config.Height))
	if format == formatWebP {
		header.bytes += int64(len(data))
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxInputSide || config.Height > maxInputSide ||
		int64(config.Width)*int64(config.Height) > int64(header.maxPixels) {
		return header, fmt.Errorf("the image is %dx%d", config.Width, config.Height)
	}
	return header, nil
}

// decodeBytesPerPixel is about how much memory decoding an image takes a
// pixel, read from its header, decoders included:
//   - PNG: 4 bytes, 8 at 16 bits (a grey image with a transparent colour
//     decodes as RGBA), twice that when interlaced: each pass is decoded
//     into an image of its own;
//   - JPEG: a byte a sample (a subsampled component has fewer samples than
//     pixels), 5 when progressive, which keeps 4-byte coefficients for each
//     sample; RGB and CMYK images add a 4-byte RGBA or CMYK copy;
//   - WebP: 8 bytes when lossless (the VP8L pixels and their copy when
//     colour-indexed); 1.5 when lossy (YCbCr 4:2:0), with a byte of alpha,
//     or 9 when the alpha is lossless. The lossy decoder also copies the
//     compressed frame (readImageHeader adds the image's bytes).
func decodeBytesPerPixel(data []byte, format string, model color.Model) float64 {
	switch format {
	case formatPNG:
		perPixel := 4.0
		switch model {
		case color.Gray16Model, color.RGBA64Model, color.NRGBA64Model:
			perPixel = 8
		}
		// The interlace method, the last byte of the IHDR chunk.
		if len(data) > 28 && data[28] != 0 {
			perPixel *= 2
		}
		return perPixel
	case formatJPEG:
		samples, progressive := jpegFrame(data)
		perPixel := samples
		if progressive {
			perPixel += 4 * samples
		}
		if model == color.RGBAModel || model == color.CMYKModel {
			perPixel += 4
		}
		return perPixel
	}
	// The chunks of a WebP after its RIFF header: an ALPH chunk (alpha,
	// lossless when its compression is 1) comes before the VP8 one.
	alpha := 0.0
	for i := 12; i+8 < len(data); {
		switch string(data[i : i+4]) {
		case "VP8L":
			return 8
		case "VP8 ":
			return 1.5 + alpha
		case "ALPH":
			alpha = 1
			if data[i+8]&3 == 1 {
				alpha = 9
			}
		}
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		i += 8 + size + size&1
	}
	return 8
}

// jpegFrame reads the frame header (SOFn) of a JPEG: the samples its
// components hold a pixel and whether it is progressive (SOF2). When it
// cannot, the most there may be: four components, progressive.
func jpegFrame(data []byte) (float64, bool) {
	for i := 2; i+4 <= len(data) && data[i] == 0xff; {
		marker := data[i+1]
		switch {
		case marker == 0xff:
			// A fill byte.
			i++
			continue
		case marker == 0 || marker >= 0xd0 && marker <= 0xd7:
			// Markers without a segment.
			i += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 {
			break
		}
		if marker < 0xc0 || marker > 0xcf || marker == 0xc4 || marker == 0xc8 || marker == 0xcc {
			i += 2 + length
			continue
		}
		// SOFn: precision, height, width, components, then an id, the
		// sampling factors (horizontal << 4 | vertical) and a table for each.
		frame := data[i+4 : min(len(data), i+2+length)]
		if len(frame) < 6 || len(frame) < 6+3*int(frame[5]) {
			break
		}
		components := frame[6 : 6+3*int(frame[5])]
		maxH, maxV, area := 1, 1, 0
		for c := 0; c < len(components); c += 3 {
			h, v := int(components[c+1]>>4), int(components[c+1]&15)
			maxH, maxV, area = max(maxH, h), max(maxV, v), area+h*v
		}
		if area == 0 {
			break
		}
		return float64(area) / float64(maxH*maxV), marker == 0xc2
	}
	return 4, true
}

// decodeImage decodes a PNG, JPEG or WebP image within readImageHeader's
// limits.
func decodeImage(data []byte) (image.Image, error) {
	header, err := readImageHeader(data)
	if err != nil {
		return nil, err
	}
	return header.decode(bytes.NewReader(data))
}

// invalidImage rejects a request's image (field names it) that is not a
// PNG, JPEG or WebP image of at most maxPixels pixels and maxInputSide a
// side.
func invalidImage(c *gin.Context, field string, maxPixels int) *types.NewAPIError {
	return badRequest(c, i18n.MsgNovelAIImageInvalid, map[string]any{"Field": field, "Max": maxPixels, "Side": maxInputSide})
}

// imageInputHeader checks the header of a request's image (field names it)
// before anything decodes it.
func imageInputHeader(c *gin.Context, data []byte, field string) (imageHeader, *types.NewAPIError) {
	header, err := readImageHeader(data)
	if err != nil {
		return header, invalidImage(c, field, header.maxPixels)
	}
	return header, nil
}

// decodeImageInput decodes a request's image whose header imageInputHeader
// checked.
func decodeImageInput(c *gin.Context, data []byte, header imageHeader, field string) (image.Image, *types.NewAPIError) {
	decoded, err := header.decode(bytes.NewReader(data))
	if err != nil {
		return nil, invalidImage(c, field, header.maxPixels)
	}
	return decoded, nil
}

// coverCrop is the centred part of bounds with the aspect ratio of width x
// height, the part CSS object-fit: cover keeps.
func coverCrop(bounds image.Rectangle, width, height int) image.Rectangle {
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
	return crop
}

// resizeBand bounds what one call of a kernel scaler holds: 32 bytes for
// each column it writes and row it reads.
const resizeBand = 1 << 20

// coverResize scales the crop of src to exactly width x height with
// CatmullRom (crop is coverCrop of the source the canvas is for): NovelAI
// requires an edit's image to match the canvas. One call of a kernel scaler
// would hold about 200 MB for a 3 MP canvas from a 4096-pixel crop, so it
// scales in two passes of calls within resizeBand: the columns of bands of
// crop rows, into an RGBA image as wide as the canvas and as high as the
// crop, then the rows of bands of its columns. Across a band the kernel
// keeps each pixel as it is.
func coverResize(src image.Image, crop image.Rectangle, width, height int) *image.RGBA {
	columns := image.NewRGBA(image.Rect(0, 0, width, crop.Dy()))
	rows := max(1, resizeBand/(32*width))
	scaler := draw.CatmullRom.NewScaler(width, rows, crop.Dx(), rows)
	for y := 0; y < crop.Dy(); y += rows {
		band := image.Rect(0, y, width, min(y+rows, crop.Dy()))
		scaler.Scale(columns, band, src, image.Rect(crop.Min.X, crop.Min.Y+band.Min.Y, crop.Max.X, crop.Min.Y+band.Max.Y), draw.Src, nil)
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	cols := max(1, resizeBand/(32*crop.Dy()))
	scaler = draw.CatmullRom.NewScaler(cols, height, cols, crop.Dy())
	for x := 0; x < width; x += cols {
		band := image.Rect(x, 0, min(x+cols, width), height)
		scaler.Scale(dst, band, columns, image.Rect(band.Min.X, 0, band.Max.X, crop.Dy()), draw.Src, nil)
	}
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
// NovelAI mask, white (brighter than mid-grey) marking the area. The mask
// covers the source image (source is its bounds), stretched when its size
// differs, so it gets the image's crop; each canvas pixel reads the mask's
// nearest pixel (as draw.NearestNeighbor picks it), then the canvas is
// snapped to the 8x8 blocks of NovelAI's latent grid: a block is white when
// more than half of it is. It reports false when no block is white.
func novelAIMask(mask image.Image, source image.Rectangle, width, height int) (string, bool) {
	bounds := mask.Bounds()
	// The decoders' image types read a pixel without boxing it in a
	// color.Color, an allocation a pixel.
	pixels, ok := mask.(image.RGBA64Image)
	if !ok {
		converted := image.NewRGBA64(bounds)
		draw.Draw(converted, bounds, mask, bounds.Min, draw.Src)
		pixels = converted
	}
	transparent := false
	for y := bounds.Min.Y; y < bounds.Max.Y && !transparent; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if pixels.RGBA64At(x, y).A == 0 {
				transparent = true
				break
			}
		}
	}
	// The image's crop, in the mask's pixels.
	crop := coverCrop(source, width, height)
	maskCrop := image.Rect(
		bounds.Min.X+(crop.Min.X-source.Min.X)*bounds.Dx()/source.Dx(),
		bounds.Min.Y+(crop.Min.Y-source.Min.Y)*bounds.Dy()/source.Dy(),
		bounds.Min.X+((crop.Max.X-source.Min.X)*bounds.Dx()+source.Dx()-1)/source.Dx(),
		bounds.Min.Y+((crop.Max.Y-source.Min.Y)*bounds.Dy()+source.Dy()-1)/source.Dy(),
	)
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	selected := false
	for blockY := 0; blockY < height; blockY += 8 {
		for blockX := 0; blockX < width; blockX += 8 {
			white, total := 0, 0
			for y := blockY; y < min(blockY+8, height); y++ {
				maskY := maskCrop.Min.Y + (2*y+1)*maskCrop.Dy()/(2*height)
				for x := blockX; x < min(blockX+8, width); x++ {
					pixel := pixels.RGBA64At(maskCrop.Min.X+(2*x+1)*maskCrop.Dx()/(2*width), maskY)
					repaint := pixel.A == 0
					if !transparent {
						// color.GrayModel's luminance.
						repaint = (19595*uint32(pixel.R)+38470*uint32(pixel.G)+7471*uint32(pixel.B)+1<<15)>>24 > 127
					}
					total++
					if repaint {
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
	invalid := invalidImage(c, field, maxInputPixels)
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
