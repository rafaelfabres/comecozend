package media

import (
	"os"
	"path/filepath"
	"testing"
)

// RetroAchievements' media host content-negotiates. Asking for webp and
// then not being able to read it meant covers that are plainly there on
// the website arrived and were thrown away as corrupt.
//
// The vectors are real files produced by libwebp, one per encoding the
// format allows: lossy VP8, lossless VP8L, and lossless with an alpha
// channel.
func TestDecodeWebP(t *testing.T) {
	for _, name := range []string{"lossy.webp", "lossless.webp", "alpha.webp"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			img, err := Decode(data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if len(img.Frames) != 1 {
				t.Fatalf("got %d frames, want 1", len(img.Frames))
			}
			b := img.Frames[0].Bounds()
			if b.Dx() != 64 || b.Dy() != 48 {
				t.Errorf("decoded %dx%d, want 64x48", b.Dx(), b.Dy())
			}
			// A decoder that returns a blank canvas would pass every
			// check above.
			var nonZero int
			for _, v := range img.Frames[0].Pix {
				if v != 0 {
					nonZero++
				}
			}
			if nonZero < len(img.Frames[0].Pix)/4 {
				t.Error("the decoded image is mostly empty")
			}
		})
	}
}

// Animated webp is the one thing the pure-Go decoder does not read. It
// has to fail as a clean rejection rather than a panic or a garbled
// frame, so the caller can fall back to another picture.
func TestAnimatedWebPIsRejectedCleanly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "anim.webp"))
	if err != nil {
		t.Skip("no animated vector")
	}
	img, err := Decode(data)
	if err == nil {
		// If a future decoder learns the format, a single still is a
		// perfectly good outcome too.
		if len(img.Frames) == 0 {
			t.Error("decoded with no frames")
		}
		return
	}
	if img != nil {
		t.Error("a failed decode should return no image")
	}
}
