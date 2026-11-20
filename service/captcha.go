package service

import (
	"bytes"
	"container/list"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The arithmetic captcha of the registration, forgot password and password
// login pages is a challenge rendered as a PNG. It only slows automated
// requests down: each image allows a single guess, and the per-network
// limits do the rest.
const (
	// CaptchaTTL is how long an issued captcha can be answered.
	CaptchaTTL = 5 * time.Minute

	captchaRedisKeyPrefix = "new-api:captcha:v1:"
	captchaRedisTimeout   = 2 * time.Second
	// captchaMemoryMaxEntries bounds the pending answers kept without Redis
	// (about 20 MB at most); every password login needs one while the login
	// captcha is on.
	captchaMemoryMaxEntries = 100000
	captchaMaxAnswerDigits  = 3

	// The image is twice its 120x40 display size so it stays sharp on HiDPI
	// screens.
	captchaWidth    = 240
	captchaHeight   = 80
	captchaFontSize = 44
)

// Captcha is an issued challenge. Its answer stays on the server.
type Captcha struct {
	ID        string `json:"captcha_id"`
	Image     string `json:"image"`      // data:image/png;base64,...
	ExpiresIn int    `json:"expires_in"` // seconds
}

type captchaChallenge struct {
	Text   string // for example "12+7=?"
	Answer int
}

// newCaptchaRand returns the random source of one captcha. A *rand.Rand is not
// safe for concurrent use, so each captcha gets its own, seeded from
// crypto/rand. Tests replace it with a seeded source.
var newCaptchaRand = func() *rand.Rand {
	var seed [32]byte
	_, _ = crand.Read(seed[:])
	return rand.New(rand.NewChaCha8(seed))
}

// NewCaptcha issues a single-use captcha and keeps its answer for CaptchaTTL:
// in Redis when it is enabled, so any node can check it, otherwise in memory.
func NewCaptcha() (*Captcha, error) {
	r := newCaptchaRand()
	challenge := newCaptchaChallenge(r)
	picture, err := renderCaptchaPNG(r, challenge.Text)
	if err != nil {
		return nil, err
	}
	id := common.GetUUID()
	if err := currentCaptchaStore().put(id, challenge.Answer); err != nil {
		return nil, err
	}
	return &Captcha{
		ID:        id,
		Image:     "data:image/png;base64," + base64.StdEncoding.EncodeToString(picture),
		ExpiresIn: int(CaptchaTTL / time.Second),
	}, nil
}

// VerifyCaptcha reports whether answer solves the captcha id. A check that
// names an existing captcha uses it up, right or wrong, so each image allows
// one guess. Store errors fail closed.
func VerifyCaptcha(id string, answer string) bool {
	if len(id) != 32 || strings.ContainsFunc(id, func(r rune) bool { return (r < '0' || r > '9') && (r < 'a' || r > 'f') }) {
		return false
	}
	expected, found, err := currentCaptchaStore().take(id)
	if err != nil {
		common.SysError(common.LogText("captcha check failed: %v", err))
		return false
	}
	if !found {
		return false
	}
	given, ok := parseCaptchaAnswer(answer)
	return ok && given == expected
}

// IssueCaptchaForTest stores a captcha with a known answer in the current
// store and returns its id, so tests in other packages can solve it.
func IssueCaptchaForTest(answer int) string {
	id := common.GetUUID()
	if err := currentCaptchaStore().put(id, answer); err != nil {
		panic(err)
	}
	return id
}

// parseCaptchaAnswer accepts one to three digits around optional spaces.
// Full-width digits, which Chinese input methods often produce, count as
// digits; anything else is a wrong answer.
func parseCaptchaAnswer(answer string) (int, bool) {
	answer = strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return '0' + (r - '０')
		}
		return r
	}, strings.TrimSpace(answer))
	if answer == "" || len(answer) > captchaMaxAnswerDigits {
		return 0, false
	}
	for _, r := range answer {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(answer)
	return value, err == nil
}

// newCaptchaChallenge picks + − × ÷ with equal chance and operands that keep
// every answer a whole number from 0 to 81: a difference is never negative
// and a division is always exact.
func newCaptchaChallenge(r *rand.Rand) captchaChallenge {
	var a, b, answer int
	var operator string
	switch r.IntN(4) {
	case 0:
		a, b = 1+r.IntN(20), 1+r.IntN(20)
		operator, answer = "+", a+b
	case 1:
		a = 2 + r.IntN(19)
		b = 1 + r.IntN(a)
		operator, answer = "-", a-b
	case 2:
		a, b = 2+r.IntN(8), 2+r.IntN(8)
		operator, answer = "×", a*b
	default:
		b = 2 + r.IntN(8)
		answer = 1 + r.IntN(9)
		a = b * answer
		operator = "÷"
	}
	return captchaChallenge{Text: fmt.Sprintf("%d%s%d=?", a, operator, b), Answer: answer}
}

// captchaStore keeps issued answers. take removes the entry it finds, which
// makes every captcha single use.
type captchaStore interface {
	put(id string, answer int) error
	take(id string) (answer int, found bool, err error)
}

// captchaStoreForTest, when set, replaces the store chosen from the Redis
// settings. Tests use it instead of changing common.RedisEnabled and
// common.RDB, which background goroutines of other code read.
var captchaStoreForTest captchaStore

func currentCaptchaStore() captchaStore {
	if captchaStoreForTest != nil {
		return captchaStoreForTest
	}
	if common.RedisEnabled && common.RDB != nil {
		return redisCaptchaStore{client: common.RDB}
	}
	return memoryCaptchas
}

// captchaTakeScript reads and deletes an answer in one step. GETDEL would need
// Redis 6.2; a script works on every supported server.
const captchaTakeScript = `
local value = redis.call('GET', KEYS[1])
if value then
  redis.call('DEL', KEYS[1])
end
return value
`

// redisCaptchaStore shares answers between nodes.
type redisCaptchaStore struct {
	client *redis.Client
}

func (s redisCaptchaStore) put(id string, answer int) error {
	ctx, cancel := context.WithTimeout(context.Background(), captchaRedisTimeout)
	defer cancel()
	return s.client.Set(ctx, captchaRedisKeyPrefix+id, strconv.Itoa(answer), CaptchaTTL).Err()
}

func (s redisCaptchaStore) take(id string) (int, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), captchaRedisTimeout)
	defer cancel()
	value, err := s.client.Eval(ctx, captchaTakeScript, []string{captchaRedisKeyPrefix + id}).Text()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	answer, err := strconv.Atoi(value)
	if err != nil {
		return 0, false, err
	}
	return answer, true, nil
}

// memoryCaptchaStore keeps answers in the process when Redis is off. Every
// entry lives CaptchaTTL, so insertion order is also expiry order: expired
// entries are dropped from the front, and past the size cap the oldest go.
type memoryCaptchaStore struct {
	mutex   sync.Mutex
	entries map[string]*list.Element
	order   *list.List // oldest first
	limit   int
	now     func() time.Time
}

type memoryCaptchaEntry struct {
	id        string
	answer    int
	expiresAt time.Time
}

var memoryCaptchas = newMemoryCaptchaStore(captchaMemoryMaxEntries, time.Now)

func newMemoryCaptchaStore(limit int, now func() time.Time) *memoryCaptchaStore {
	return &memoryCaptchaStore{entries: map[string]*list.Element{}, order: list.New(), limit: limit, now: now}
}

func (store *memoryCaptchaStore) put(id string, answer int) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	now := store.now()
	for oldest := store.order.Front(); oldest != nil && !now.Before(oldest.Value.(*memoryCaptchaEntry).expiresAt); oldest = store.order.Front() {
		store.remove(oldest)
	}
	if existing, ok := store.entries[id]; ok {
		store.remove(existing)
	}
	// A flood evicts the oldest challenges; their users fetch a new image.
	for store.order.Len() >= store.limit {
		store.remove(store.order.Front())
	}
	store.entries[id] = store.order.PushBack(&memoryCaptchaEntry{id: id, answer: answer, expiresAt: now.Add(CaptchaTTL)})
	return nil
}

func (store *memoryCaptchaStore) take(id string) (int, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	element, ok := store.entries[id]
	if !ok {
		return 0, false, nil
	}
	store.remove(element)
	entry := element.Value.(*memoryCaptchaEntry)
	if !store.now().Before(entry.expiresAt) {
		return 0, false, nil
	}
	return entry.answer, true, nil
}

// remove drops one entry. The caller holds the mutex.
func (store *memoryCaptchaStore) remove(element *list.Element) {
	store.order.Remove(element)
	delete(store.entries, element.Value.(*memoryCaptchaEntry).id)
}

// captchaCanvas draws one captcha image.
type captchaCanvas struct {
	r          *rand.Rand
	dst        *image.RGBA
	rasterizer *vector.Rasterizer
}

// renderCaptchaPNG draws text with a random rotation, scale, shear, offset and
// colour per glyph between layers of noise, and encodes it as PNG.
func renderCaptchaPNG(r *rand.Rand, text string) ([]byte, error) {
	canvas := &captchaCanvas{
		r:          r,
		dst:        image.NewRGBA(image.Rect(0, 0, captchaWidth, captchaHeight)),
		rasterizer: vector.NewRasterizer(captchaWidth, captchaHeight),
	}
	background := captchaHSV(r.Float64()*360, captchaRange(r, 0.05, 0.15), captchaRange(r, 0.93, 1))
	xdraw.Draw(canvas.dst, canvas.dst.Bounds(), image.NewUniform(background), image.Point{}, xdraw.Src)

	// Faint clutter under the text.
	canvas.dots(72, 6, 60, 140, 0.8, 2.2)
	for range 2 {
		canvas.curve(captchaRange(r, 1.2, 2), 120, 120)
	}
	if err := canvas.glyphs(text); err != nil {
		return nil, err
	}
	// Noise across the text in the glyphs' colour family, so colour alone does
	// not separate it from them.
	canvas.curve(captchaRange(r, 2.4, 3.2), 160, 210)
	canvas.curve(captchaRange(r, 1.2, 1.8), 160, 210)
	canvas.dots(40, 4, 200, 255, 0.8, 1.8)

	var encoded bytes.Buffer
	if err := captchaPNGEncoder.Encode(&encoded, canvas.dst); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

// glyphs lays the text out over about 78% of the width and draws each glyph
// through its own affine transform.
func (canvas *captchaCanvas) glyphs(text string) error {
	r := canvas.r
	runes := []rune(text)
	masks := make([]*image.Alpha, len(runes))
	scales := make([]float64, len(runes))
	total := 0.0
	for i, ch := range runes {
		mask, err := captchaGlyphMask(ch)
		if err != nil {
			return err
		}
		masks[i] = mask
		scales[i] = captchaRange(r, 0.82, 1.05)
		total += float64(mask.Bounds().Dx()) * scales[i]
	}
	gap := (captchaWidth*0.78 - total) / float64(max(len(masks)-1, 1))
	gap = math.Max(-4, math.Min(gap, 14))
	total += gap * float64(len(masks)-1)
	fit := math.Min(1, (captchaWidth-12)/total) // shrink text that would not fit
	x := (captchaWidth-total*fit)/2 + captchaRange(r, -6, 6)
	for i, mask := range masks {
		scale := scales[i] * fit
		width, height := float64(mask.Bounds().Dx()), float64(mask.Bounds().Dy())
		theta := captchaRange(r, -0.40, 0.40) // about ±23 degrees
		shear := captchaRange(r, -0.20, 0.20)
		centerX := x + width*scale/2
		centerY := captchaHeight/2 + captchaRange(r, -6, 6)
		cos, sin := math.Cos(theta)*scale, math.Sin(theta)*scale
		// dst = translate(center) · rotate·scale·shear · translate(-size/2) · src
		a, b := cos, -sin+cos*shear
		c, d := sin, cos+sin*shear
		matrix := f64.Aff3{
			a, b, centerX - (a*width/2 + b*height/2),
			c, d, centerY - (c*width/2 + d*height/2),
		}
		// A premultiplied colour tile keeps Transform on its *image.RGBA fast path.
		ink := captchaInk(r)
		tile := image.NewRGBA(mask.Bounds())
		for j, alpha := range mask.Pix {
			tile.Pix[4*j] = uint8(uint16(ink.R) * uint16(alpha) / 255)
			tile.Pix[4*j+1] = uint8(uint16(ink.G) * uint16(alpha) / 255)
			tile.Pix[4*j+2] = uint8(uint16(ink.B) * uint16(alpha) / 255)
			tile.Pix[4*j+3] = alpha
		}
		xdraw.BiLinear.Transform(canvas.dst, matrix, tile, tile.Bounds(), xdraw.Over, nil)
		x += width*scale + (gap+captchaRange(r, -2, 2))*fit
	}
	return nil
}

// curve strokes a band of the given width along a random cubic Bézier curve
// that crosses the image.
func (canvas *captchaCanvas) curve(width, alphaLow, alphaHigh float64) {
	r := canvas.r
	w, h := float64(captchaWidth), float64(captchaHeight)
	control := [4][2]float64{
		{captchaRange(r, -10, w*0.15), captchaRange(r, h*0.2, h*0.8)},
		{captchaRange(r, w*0.2, w*0.45), captchaRange(r, -h*0.2, h*1.2)},
		{captchaRange(r, w*0.55, w*0.8), captchaRange(r, -h*0.2, h*1.2)},
		{captchaRange(r, w*0.85, w+10), captchaRange(r, h*0.2, h*0.8)},
	}
	const steps = 48
	var points, left, right [steps + 1][2]float64
	for i := range steps + 1 {
		t := float64(i) / steps
		u := 1 - t
		for axis := range 2 {
			points[i][axis] = u*u*u*control[0][axis] + 3*u*u*t*control[1][axis] + 3*u*t*t*control[2][axis] + t*t*t*control[3][axis]
		}
	}
	for i := range steps + 1 {
		from, to := points[max(i-1, 0)], points[min(i+1, steps)]
		dx, dy := to[0]-from[0], to[1]-from[1]
		length := math.Hypot(dx, dy)
		if length == 0 {
			length = 1
		}
		nx, ny := -dy/length*width/2, dx/length*width/2
		left[i] = [2]float64{points[i][0] + nx, points[i][1] + ny}
		right[i] = [2]float64{points[i][0] - nx, points[i][1] - ny}
	}
	canvas.rasterizer.MoveTo(float32(left[0][0]), float32(left[0][1]))
	for i := 1; i <= steps; i++ {
		canvas.rasterizer.LineTo(float32(left[i][0]), float32(left[i][1]))
	}
	for i := steps; i >= 0; i-- {
		canvas.rasterizer.LineTo(float32(right[i][0]), float32(right[i][1]))
	}
	canvas.rasterizer.ClosePath()
	ink := captchaInk(r)
	ink.A = uint8(captchaRange(r, alphaLow, alphaHigh))
	canvas.paint(ink)
}

// dots scatters count round dots in the given number of colour batches, one
// rasterizer pass per batch.
func (canvas *captchaCanvas) dots(count, batches int, alphaLow, alphaHigh, radiusLow, radiusHigh float64) {
	r := canvas.r
	const sides = 10
	for range batches {
		for range count / batches {
			centerX, centerY := captchaRange(r, 0, captchaWidth), captchaRange(r, 0, captchaHeight)
			radius := captchaRange(r, radiusLow, radiusHigh)
			canvas.rasterizer.MoveTo(float32(centerX+radius), float32(centerY))
			for i := 1; i <= sides; i++ {
				angle := 2 * math.Pi * float64(i) / sides
				canvas.rasterizer.LineTo(float32(centerX+radius*math.Cos(angle)), float32(centerY+radius*math.Sin(angle)))
			}
			canvas.rasterizer.ClosePath()
		}
		ink := captchaInk(r)
		ink.A = uint8(captchaRange(r, alphaLow, alphaHigh))
		canvas.paint(ink)
	}
}

// paint fills everything added to the rasterizer with ink and resets it.
func (canvas *captchaCanvas) paint(ink color.Color) {
	canvas.rasterizer.Draw(canvas.dst, canvas.dst.Bounds(), image.NewUniform(ink), image.Point{})
	canvas.rasterizer.Reset(captchaWidth, captchaHeight)
}

var (
	captchaFaceOnce sync.Once
	captchaFace     font.Face
	captchaFaceErr  error
	// captchaFaceMutex guards captchaFace, which is not safe for concurrent
	// use. It is only taken when a glyph is not cached yet.
	captchaFaceMutex sync.Mutex
	captchaGlyphs    sync.Map // rune -> *image.Alpha, never changed once stored
)

// captchaGlyphMask returns the tight alpha mask of ch in Go Bold, rendering it
// on first use.
func captchaGlyphMask(ch rune) (*image.Alpha, error) {
	if mask, ok := captchaGlyphs.Load(ch); ok {
		return mask.(*image.Alpha), nil
	}
	captchaFaceOnce.Do(func() {
		parsed, err := opentype.Parse(gobold.TTF)
		if err != nil {
			captchaFaceErr = err
			return
		}
		captchaFace, captchaFaceErr = opentype.NewFace(parsed, &opentype.FaceOptions{Size: captchaFontSize, DPI: 72, Hinting: font.HintingNone})
	})
	if captchaFaceErr != nil {
		return nil, captchaFaceErr
	}
	captchaFaceMutex.Lock()
	defer captchaFaceMutex.Unlock()
	bounds, _, ok := captchaFace.GlyphBounds(ch)
	if !ok {
		return nil, fmt.Errorf("captcha font has no glyph for %q", ch)
	}
	const padding = 2
	minX, minY := bounds.Min.X.Floor()-padding, bounds.Min.Y.Floor()-padding
	maxX, maxY := bounds.Max.X.Ceil()+padding, bounds.Max.Y.Ceil()+padding
	mask := image.NewAlpha(image.Rect(0, 0, maxX-minX, maxY-minY))
	drawer := font.Drawer{Dst: mask, Src: image.Opaque, Face: captchaFace, Dot: fixed.P(-minX, -minY)}
	drawer.DrawString(string(ch))
	stored, _ := captchaGlyphs.LoadOrStore(ch, mask)
	return stored.(*image.Alpha), nil
}

// captchaInk is a dark, saturated colour of random hue for glyphs and noise.
func captchaInk(r *rand.Rand) color.NRGBA {
	return captchaHSV(r.Float64()*360, captchaRange(r, 0.55, 0.9), captchaRange(r, 0.25, 0.55))
}

// captchaHSV converts a hue in degrees, a saturation and a value to an opaque
// colour.
func captchaHSV(hue, saturation, value float64) color.NRGBA {
	chroma := value * saturation
	x := chroma * (1 - math.Abs(math.Mod(hue/60, 2)-1))
	m := value - chroma
	var red, green, blue float64
	switch {
	case hue < 60:
		red, green, blue = chroma, x, 0
	case hue < 120:
		red, green, blue = x, chroma, 0
	case hue < 180:
		red, green, blue = 0, chroma, x
	case hue < 240:
		red, green, blue = 0, x, chroma
	case hue < 300:
		red, green, blue = x, 0, chroma
	default:
		red, green, blue = chroma, 0, x
	}
	return color.NRGBA{R: uint8((red + m) * 255), G: uint8((green + m) * 255), B: uint8((blue + m) * 255), A: 255}
}

func captchaRange(r *rand.Rand, low, high float64) float64 {
	return low + r.Float64()*(high-low)
}

// captchaPNGBuffers lets the encoder reuse its zlib writer and scanline
// buffers between images, which otherwise dominate the allocations.
type captchaPNGBuffers struct{ pool sync.Pool }

func (buffers *captchaPNGBuffers) Get() *png.EncoderBuffer {
	buffer, _ := buffers.pool.Get().(*png.EncoderBuffer)
	return buffer
}

func (buffers *captchaPNGBuffers) Put(buffer *png.EncoderBuffer) {
	buffers.pool.Put(buffer)
}

var captchaPNGEncoder = png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: &captchaPNGBuffers{}}
