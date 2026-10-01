package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	stdraw "image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"

	// webp as well: a CDN may serve it whatever the Accept header asks
	// for, and a picture that arrives in a format nothing can read is
	// indistinguishable from a picture that is not there.
	_ "golang.org/x/image/webp"
	"io"
	"time"

	xdraw "golang.org/x/image/draw"
)

const (
	MaxGIFFrames        = 16
	MaxGIFSourceFrames  = 128
	MaxGIFSourcePixels  = 1280 * 1280
	MaxGIFDecodedPixels = MaxGIFFrames * MaxGIFSourcePixels
	// MaxStillPixels is the limit for a single (non-animated) picture. itch.io
	// page images are often 1500x1500 originals; decoded once and scaled down
	// to MaxImageWidth, that is ~10 MB for a moment, fine on a 1 GB device.
	MaxStillPixels    = 2600 * 2600
	MaxImageWidth     = 640
	DefaultFrameDelay = 100 * time.Millisecond
	// MaxSourceBytes caps how many bytes of an encoded image we accept from an
	// untrusted (game-author-controlled) URL, so a very large download cannot
	// exhaust memory on a 1GB device before it is even decoded.
	MaxSourceBytes = 16 << 20 // 16 MiB
)

// ErrRejected marks deterministic content failures. Callers may suppress the
// same source until an explicit cache reset instead of retrying it every frame.
var ErrRejected = errors.New("image content rejected")

// DecodedImage is renderer-independent decoded artwork. Animated GIFs retain
// their composited frames and timing so each GUI backend can upload them using
// its own texture API.
type DecodedImage struct {
	Frames []*image.RGBA
	Delays []time.Duration
}

// ReadSource reads an untrusted encoded image with the common byte limit. A
// declared oversized response is rejected without reading its body; the
// streaming cap still protects responses with an absent or incorrect length.
func ReadSource(reader io.Reader, contentLength int64) ([]byte, error) {
	if contentLength > MaxSourceBytes {
		return nil, rejected("image exceeds %d-byte cap", MaxSourceBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image source: %w", err)
	}
	if len(data) > MaxSourceBytes {
		return nil, rejected("image exceeds %d-byte cap", MaxSourceBytes)
	}
	return data, nil
}

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

func Decode(data []byte) (*DecodedImage, error) {
	// Reject wildly oversized images before decoding: a small but bomb-crafted
	// PNG/JPEG/GIF can declare enormous dimensions and blow past memory on a 1GB
	// device once image.Decode allocates the pixel buffer. DecodeConfig only
	// reads the header, so this is cheap.
	cfg, format, cfgErr := image.DecodeConfig(bytes.NewReader(data))
	if cfgErr != nil {
		return nil, rejected("decode image config: %v", cfgErr)
	}
	limit := int64(MaxStillPixels)
	if format == "gif" {
		limit = int64(MaxGIFSourcePixels)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > limit {
		return nil, rejected("decode image: unsafe dimensions %dx%d", cfg.Width, cfg.Height)
	}

	if format == "gif" {
		preflight, err := preflightGIF(data)
		if err != nil {
			return nil, err
		}
		if preflight.frames > 1 {
			animated, err := gif.DecodeAll(bytes.NewReader(data))
			if err != nil {
				return nil, rejected("decode GIF: %v", err)
			}
			return decodeGIF(animated)
		}
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, rejected("decode image: %v", err)
	}
	return &DecodedImage{
		Frames: []*image.RGBA{scaleRGBA(decoded)},
		Delays: []time.Duration{DefaultFrameDelay},
	}, nil
}

type gifPreflight struct {
	frames int
	pixels int64
}

// preflightGIF walks only the GIF container structure. It counts the paletted
// frame buffers that gif.DecodeAll would allocate without expanding LZW data.
func preflightGIF(data []byte) (gifPreflight, error) {
	var result gifPreflight
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return result, rejected("decode GIF: invalid or truncated header")
	}
	canvasWidth := int(binary.LittleEndian.Uint16(data[6:8]))
	canvasHeight := int(binary.LittleEndian.Uint16(data[8:10]))
	if canvasWidth <= 0 || canvasHeight <= 0 ||
		int64(canvasWidth)*int64(canvasHeight) > int64(MaxGIFSourcePixels) {
		return result, rejected("decode GIF: unsafe dimensions %dx%d", canvasWidth, canvasHeight)
	}

	offset := 13
	if data[10]&0x80 != 0 {
		var err error
		offset, err = skipGIFBytes(data, offset, gifColorTableSize(data[10]))
		if err != nil {
			return result, err
		}
	}

	for {
		if offset >= len(data) {
			return result, rejected("decode GIF: missing trailer")
		}
		blockType := data[offset]
		offset++
		switch blockType {
		case 0x3b: // trailer
			if result.frames == 0 {
				return result, rejected("decode GIF: no image frames")
			}
			return result, nil
		case 0x21: // extension
			if offset >= len(data) {
				return result, rejected("decode GIF: truncated extension")
			}
			offset++ // extension label
			var err error
			offset, err = skipGIFSubBlocks(data, offset)
			if err != nil {
				return result, err
			}
		case 0x2c: // image descriptor
			if len(data)-offset < 9 {
				return result, rejected("decode GIF: truncated image descriptor")
			}
			left := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
			top := int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
			width := int(binary.LittleEndian.Uint16(data[offset+4 : offset+6]))
			height := int(binary.LittleEndian.Uint16(data[offset+6 : offset+8]))
			packed := data[offset+8]
			offset += 9
			if width <= 0 || height <= 0 || left+width > canvasWidth || top+height > canvasHeight {
				return result, rejected("decode GIF: unsafe frame bounds %d,%d %dx%d", left, top, width, height)
			}

			result.frames++
			if result.frames > MaxGIFSourceFrames {
				return result, rejected("decode GIF: source has more than %d frames", MaxGIFSourceFrames)
			}
			result.pixels += int64(width) * int64(height)
			if result.pixels > int64(MaxGIFDecodedPixels) {
				return result, rejected("decode GIF: cumulative frame pixels exceed %d", MaxGIFDecodedPixels)
			}

			if packed&0x80 != 0 {
				var err error
				offset, err = skipGIFBytes(data, offset, gifColorTableSize(packed))
				if err != nil {
					return result, err
				}
			}
			if offset >= len(data) {
				return result, rejected("decode GIF: missing LZW code size")
			}
			if data[offset] < 2 || data[offset] > 8 {
				return result, rejected("decode GIF: invalid LZW code size %d", data[offset])
			}
			offset++
			var err error
			offset, err = skipGIFSubBlocks(data, offset)
			if err != nil {
				return result, err
			}
		default:
			return result, rejected("decode GIF: unexpected block 0x%02x", blockType)
		}
	}
}

func gifColorTableSize(packed byte) int {
	return 3 * (1 << ((packed & 0x07) + 1))
}

func skipGIFBytes(data []byte, offset, count int) (int, error) {
	if count < 0 || offset < 0 || count > len(data)-offset {
		return offset, rejected("decode GIF: truncated color table")
	}
	return offset + count, nil
}

func skipGIFSubBlocks(data []byte, offset int) (int, error) {
	for {
		if offset >= len(data) {
			return offset, rejected("decode GIF: truncated data blocks")
		}
		length := int(data[offset])
		offset++
		if length == 0 {
			return offset, nil
		}
		if length > len(data)-offset {
			return offset, rejected("decode GIF: truncated data block")
		}
		offset += length
	}
}

func decodeGIF(source *gif.GIF) (*DecodedImage, error) {
	if len(source.Image) == 0 {
		return nil, rejected("decode image: empty GIF")
	}
	w, h := source.Config.Width, source.Config.Height
	if w <= 0 || h <= 0 {
		bounds := source.Image[0].Bounds()
		w, h = bounds.Max.X, bounds.Max.Y
	}
	if w <= 0 || h <= 0 || int64(w)*int64(h) > int64(MaxGIFSourcePixels) {
		return nil, rejected("decode image: unsafe GIF dimensions %dx%d", w, h)
	}

	total := len(source.Image)
	stored := total
	if stored > MaxGIFFrames {
		stored = MaxGIFFrames
	}
	samples := make([]int, stored)
	sampleSlots := make(map[int]int, stored)
	delays := make([]time.Duration, stored)
	for index := range samples {
		samples[index] = index * total / stored
		sampleSlots[samples[index]] = index
	}
	for index, start := range samples {
		end := total
		if index+1 < len(samples) {
			end = samples[index+1]
		}
		centiseconds := 0
		for frame := start; frame < end && frame < len(source.Delay); frame++ {
			centiseconds += source.Delay[frame]
		}
		if centiseconds == 0 {
			delays[index] = DefaultFrameDelay
		} else {
			delays[index] = time.Duration(centiseconds) * 10 * time.Millisecond
		}
	}

	bounds := image.Rect(0, 0, w, h)
	background := color.Color(color.RGBA{A: 255})
	if palette, ok := source.Config.ColorModel.(color.Palette); ok && int(source.BackgroundIndex) < len(palette) {
		background = palette[source.BackgroundIndex]
	}
	fill := image.NewUniform(background)
	canvas := image.NewRGBA(bounds)
	stdraw.Draw(canvas, bounds, fill, image.Point{}, stdraw.Src)
	var previous *image.RGBA
	frames := make([]*image.RGBA, stored)

	for index, frame := range source.Image {
		disposal := byte(gif.DisposalNone)
		if index < len(source.Disposal) {
			disposal = source.Disposal[index]
		}
		if disposal == gif.DisposalPrevious {
			if previous == nil {
				previous = image.NewRGBA(bounds)
			}
			stdraw.Draw(previous, bounds, canvas, image.Point{}, stdraw.Src)
		}
		stdraw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, stdraw.Over)
		if slot, ok := sampleSlots[index]; ok {
			frames[slot] = scaleRGBA(canvas)
		}
		switch disposal {
		case gif.DisposalBackground:
			stdraw.Draw(canvas, frame.Bounds(), fill, image.Point{}, stdraw.Src)
		case gif.DisposalPrevious:
			if previous != nil {
				stdraw.Draw(canvas, frame.Bounds(), previous, frame.Bounds().Min, stdraw.Src)
			}
		}
	}
	return &DecodedImage{Frames: frames, Delays: delays}, nil
}

func scaleRGBA(source image.Image) *image.RGBA {
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > MaxImageWidth {
		h = h * MaxImageWidth / w
		w = MaxImageWidth
	}
	destination := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == bounds.Dx() && h == bounds.Dy() {
		stdraw.Draw(destination, destination.Bounds(), source, bounds.Min, stdraw.Src)
	} else {
		xdraw.NearestNeighbor.Scale(destination, destination.Bounds(), source, bounds, xdraw.Src, nil)
	}
	return destination
}
