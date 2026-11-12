// Package content fetches slides from connector sources, turns every slide
// into a screen-ready JPEG, and caches the result so screens keep showing the
// last good content when a source is down.
package content

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // register decoders
	"image/jpeg"
	_ "image/png"
	"io"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp" // register decoder
)

const (
	// ScreenW and ScreenH are the size slides are prepared for.
	ScreenW = 1920
	ScreenH = 1080
	// MaxImageBytes caps a downloaded image.
	MaxImageBytes = 25 << 20
	// maxPixels rejects decompression bombs (a tiny file claiming a huge size).
	maxPixels   = 50_000_000
	jpegQuality = 85
)

// ErrNotImage is returned when the bytes aren't a supported image.
var ErrNotImage = errors.New("not a supported image (jpeg, png, gif, webp)")

// ProcessImage decodes an untrusted image, scales it down to fit the screen,
// and re-encodes it as JPEG. Screens never see the original bytes.
func ProcessImage(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxImageBytes {
		return nil, fmt.Errorf("image is over %d MB", MaxImageBytes>>20)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("image is %dx%d; the limit is %d megapixels", cfg.Width, cfg.Height, maxPixels/1_000_000)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrNotImage
	}
	return encodeJPEG(fit(src, ScreenW, ScreenH))
}

// fit scales img down (never up) to fit within w x h, keeping aspect ratio.
func fit(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= w && sh <= h {
		return img
	}
	scale := min(float64(w)/float64(sw), float64(h)/float64(sh))
	dw, dh := max(1, int(float64(sw)*scale)), max(1, int(float64(sh)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func encodeJPEG(img image.Image) ([]byte, error) {
	// JPEG has no alpha: flatten onto black so transparent PNGs look right.
	b := img.Bounds()
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, image.NewUniform(color.Black), image.Point{}, draw.Src)
	draw.Draw(flat, b, img, b.Min, draw.Over)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, flat, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// TextStyle controls rendered text slides. A theme sets these; anything
// left unset falls back to DefaultTextStyle and the built-in fonts.
type TextStyle struct {
	Background color.Color
	Title      color.Color
	Body       color.Color
	Accent     color.Color
	// TitleFace and BodyFace come from TextFaces.
	TitleFace font.Face
	BodyFace  font.Face
}

// DefaultTextStyle is DoxaBoard's own look: cool near-black with the brand
// blue as the accent. It reads well on any TV. See docs/brand.md.
var DefaultTextStyle = TextStyle{
	Background: color.RGBA{0x10, 0x14, 0x1a, 0xff},
	Title:      color.RGBA{0xf2, 0xf5, 0xf9, 0xff},
	Body:       color.RGBA{0xc3, 0xca, 0xd6, 0xff},
	Accent:     color.RGBA{0x6a, 0xa8, 0xff, 0xff},
}

// withDefaults fills unset colors from DefaultTextStyle.
func (s TextStyle) withDefaults() TextStyle {
	d := DefaultTextStyle
	if s.Background == nil {
		s.Background = d.Background
	}
	if s.Title == nil {
		s.Title = d.Title
	}
	if s.Body == nil {
		s.Body = d.Body
	}
	if s.Accent == nil {
		s.Accent = d.Accent
	}
	return s
}

var (
	titleFace, bodyFace font.Face
	faceErr             error
)

const (
	titleSize = 84
	bodySize  = 50
)

func init() {
	titleFace, bodyFace, faceErr = TextFaces(nil, nil)
}

// TextFaces builds the title and body faces for text slides from TTF or
// OTF data, at the sizes the layout expects. A nil font means the built-in
// one.
func TextFaces(titleTTF, bodyTTF []byte) (title, body font.Face, err error) {
	if titleTTF == nil {
		titleTTF = gobold.TTF
	}
	if bodyTTF == nil {
		bodyTTF = goregular.TTF
	}
	if title, err = newFace(titleTTF, titleSize); err != nil {
		return nil, nil, fmt.Errorf("title font: %w", err)
	}
	if body, err = newFace(bodyTTF, bodySize); err != nil {
		return nil, nil, fmt.Errorf("body font: %w", err)
	}
	return title, body, nil
}

func newFace(ttf []byte, size float64) (font.Face, error) {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// RenderText draws a title and body onto a screen-sized JPEG, for slides
// that have no image (e.g. an RSS post without a picture).
func RenderText(title, body string, style TextStyle) ([]byte, error) {
	if faceErr != nil {
		return nil, faceErr
	}
	style = style.withDefaults()
	titleFace, bodyFace := titleFace, bodyFace
	if style.TitleFace != nil {
		titleFace = style.TitleFace
	}
	if style.BodyFace != nil {
		bodyFace = style.BodyFace
	}
	img := image.NewRGBA(image.Rect(0, 0, ScreenW, ScreenH))
	draw.Draw(img, img.Bounds(), image.NewUniform(style.Background), image.Point{}, draw.Src)

	const margin = 160
	width := ScreenW - 2*margin
	titleLines := clampLines(wrap(titleFace, title, width), 3)
	bodyLines := clampLines(wrap(bodyFace, body, width), 8)

	tlh := titleFace.Metrics().Height.Ceil() + 12
	blh := bodyFace.Metrics().Height.Ceil() + 14
	gap := 0
	if len(titleLines) > 0 && len(bodyLines) > 0 {
		gap = 70
	}
	total := len(titleLines)*tlh + gap + len(bodyLines)*blh
	y := (ScreenH - total) / 2

	// Accent bar to the left of the text block.
	draw.Draw(img, image.Rect(margin-48, y, margin-36, y+total), image.NewUniform(style.Accent), image.Point{}, draw.Src)

	d := &font.Drawer{Dst: img}
	d.Face, d.Src = titleFace, image.NewUniform(style.Title)
	for _, line := range titleLines {
		y += tlh
		d.Dot = fixed.P(margin, y-12)
		d.DrawString(line)
	}
	y += gap
	d.Face, d.Src = bodyFace, image.NewUniform(style.Body)
	for _, line := range bodyLines {
		y += blh
		d.Dot = fixed.P(margin, y-14)
		d.DrawString(line)
	}
	return encodeJPEG(img)
}

// wrap breaks text into lines no wider than width pixels.
func wrap(face font.Face, text string, width int) []string {
	var lines []string
	limit := fixed.I(width)
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			// Break single words longer than a line (URLs, etc.).
			for font.MeasureString(face, w) > limit {
				cut := fitPrefix(face, w, limit)
				if line != "" {
					lines = append(lines, line)
					line = ""
				}
				lines = append(lines, w[:cut])
				w = w[cut:]
			}
			candidate := w
			if line != "" {
				candidate = line + " " + w
			}
			if font.MeasureString(face, candidate) <= limit {
				line = candidate
				continue
			}
			lines = append(lines, line)
			line = w
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// fitPrefix returns the byte length of the longest prefix of w (on a rune
// boundary, at least one rune) that fits in limit.
func fitPrefix(face font.Face, w string, limit fixed.Int26_6) int {
	cut := 0
	for i := range w {
		if i > 0 && font.MeasureString(face, w[:i]) > limit {
			break
		}
		cut = i
	}
	if cut == 0 {
		for i := range w {
			if i > 0 {
				return i
			}
		}
		return len(w)
	}
	return cut
}

func clampLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n]...)
	out[n-1] = strings.TrimRight(out[n-1], " .,;:") + "…"
	return out
}
