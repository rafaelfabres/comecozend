package sdlui

/*
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	xdraw "golang.org/x/image/draw"

	"leaf-mlp1-poc/internal/media"
)

// maxTextures must match MAX_TEXTURES in bridge.c. Animated covers need one
// texture per frame (internal/media caps a decoded GIF at MaxGIFFrames = 16),
// so the table is sized for a couple of animations plus the stills around
// them. Leaf does the same thing — its animatedTexture holds a Texture per
// frame — just with its own renderer.
const maxTextures = 56

// fetchConcurrency mirrors Leaf's own limit (its ImageCache uses a size-2
// semaphore): cover art must never compete with catalog paging for what
// little bandwidth the device has.
const fetchConcurrency = 2

type cacheEntry struct {
	url      string
	slots    []int           // one texture per frame; len==1 for stills
	delays   []time.Duration // per-frame delay, parallel to slots
	started  time.Time       // when this entry's animation clock began
	lastUsed time.Time
}

// frameAt returns the texture slot to draw now, and whether this entry
// animates at all.
func (e *cacheEntry) frameAt(now time.Time) (int, bool) {
	if len(e.slots) <= 1 {
		return e.slots[0], false
	}
	var total time.Duration
	for _, d := range e.delays {
		total += d
	}
	if total <= 0 {
		return e.slots[0], false
	}
	elapsed := now.Sub(e.started) % total
	for i, d := range e.delays {
		if elapsed < d {
			return e.slots[i], true
		}
		elapsed -= d
	}
	return e.slots[len(e.slots)-1], true
}

type decoded struct {
	url       string
	image     *media.DecodedImage
	err       error
	transient bool // network trouble: worth another try later
}

// ImageCache downloads and decodes cover art on worker goroutines, then
// uploads it as an SDL texture on the GUI thread. The split matters: SDL
// texture creation must happen on the thread that owns the window (the same
// constraint that forces runtime.LockOSThread in main), so workers only ever
// produce renderer-independent *image.RGBA via internal/media.
type ImageCache struct {
	mu       sync.Mutex
	entries  map[string]*cacheEntry
	freeSlot []int
	fetching map[string]bool
	failed   map[string]bool
	// retryAt holds covers that failed on a network error (connection reset,
	// timeout, 5xx): they are tried again after this time instead of being
	// given up for the whole session.
	retryAt map[string]time.Time

	ready  chan decoded
	sem    chan struct{}
	client *http.Client

	// animating is set while a frame of an animated entry was served this
	// frame, so the caller knows a redraw is due. It is cleared by
	// Animating(), which the draw loop calls once per frame.
	animating bool
}

func NewImageCache() *ImageCache {
	free := make([]int, 0, maxTextures)
	for i := 0; i < maxTextures; i++ {
		free = append(free, i)
	}
	return &ImageCache{
		entries:  make(map[string]*cacheEntry),
		freeSlot: free,
		fetching: make(map[string]bool),
		failed:   make(map[string]bool),
		retryAt:  make(map[string]time.Time),
		ready:    make(chan decoded, 16),
		sem:      make(chan struct{}, fetchConcurrency),
		client:   &http.Client{Timeout: 20 * time.Second, Transport: ipv4FirstTransport()},
	}
}

// Slot returns the texture slot holding url's artwork, or -1 if it isn't
// ready. A miss kicks off a background fetch, so simply drawing a screen
// repeatedly is enough to make artwork appear once it arrives.
func (c *ImageCache) Slot(url string) int {
	if url == "" {
		return -1
	}
	c.mu.Lock()
	if entry, ok := c.entries[url]; ok {
		now := time.Now()
		entry.lastUsed = now
		slot, animated := entry.frameAt(now)
		if animated {
			c.animating = true
		}
		c.mu.Unlock()
		return slot
	}
	if t, ok := c.retryAt[url]; ok && time.Now().After(t) {
		delete(c.retryAt, url)
		delete(c.failed, url)
	}
	if c.fetching[url] || c.failed[url] {
		c.mu.Unlock()
		return -1
	}
	c.fetching[url] = true
	c.mu.Unlock()

	go c.fetch(url)
	return -1
}

// Failed reports whether url was tried and rejected, so a screen can show a
// "no image" state instead of a spinner that never resolves.
func (c *ImageCache) Failed(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed[url]
}

// Warm pre-fetches artwork without needing it this frame — used to load the
// neighbours of the selected row so scrolling doesn't stutter.
func (c *ImageCache) Warm(url string) { c.Slot(url) }

func (c *ImageCache) fetch(url string) {
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	result := decoded{url: url}
	defer func() {
		select {
		case c.ready <- result:
		case <-time.After(5 * time.Second):
			// The GUI thread isn't draining; drop it rather than leaking
			// this goroutine. The next Slot() call will retry.
			c.mu.Lock()
			delete(c.fetching, url)
			c.mu.Unlock()
		}
	}()

	// Image CDNs (RetroAchievements' media host among them) answer 403 to
	// Go's default User-Agent, so ask like a browser would.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		result.err = err
		return
	}
	req.Header.Set("User-Agent", BrowserUserAgent)
	// Only formats this build can decode. Advertising avif and webp was
	// an invitation the CDN accepted, and every negotiated cover then
	// failed to decode and was marked dead.
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif;q=0.9,*/*;q=0.1")
	if strings.Contains(url, "retroachievements.org") {
		req.Header.Set("Referer", "https://retroachievements.org/")
	}
	// Connection resets from the image CDN are common on this network: try
	// up to three times before giving up for now.
	var data []byte
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		resp, err := c.client.Do(req.Clone(req.Context()))
		if err != nil {
			result.err, result.transient = err, true
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			result.err = fmt.Errorf("HTTP %d", resp.StatusCode)
			result.transient = resp.StatusCode >= 500 || resp.StatusCode == 429
			if result.transient {
				continue
			}
			return
		}
		// internal/media enforces the byte cap — all renderer-independent,
		// which is exactly why it can run here off the GUI thread.
		data, err = media.ReadSource(resp.Body, resp.ContentLength)
		resp.Body.Close()
		if err != nil {
			if errors.Is(err, media.ErrRejected) {
				result.err, result.transient = err, false
				return
			}
			result.err, result.transient = err, true
			continue
		}
		result.err, result.transient = nil, false
		break
	}
	if result.err != nil {
		return
	}
	result.image, result.err = decodeStill(data)
}

// decodeStill produces a single frame for display.
//
// media.Decode composites every frame of an animated GIF, and enforces limits
// (128 frames, 25 MP total) that reject a lot of real itch.io cover art —
// those long animated banners are exactly what was failing with "source has
// more than 128 frames". Nothing here animates: only frame 0 is ever
// uploaded as a texture. So for GIFs we decode just the first frame with the
// standard library, which ignores frame count entirely and costs a fraction
// of the memory, then downscale it like media does.
func decodeStill(data []byte) (*media.DecodedImage, error) {
	if decoded, err := media.Decode(data); err == nil {
		return decoded, nil
	} else if !isGIF(data) {
		return nil, err
	}
	// gif.Decode returns only the first frame, with no animation limits.
	first, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode GIF first frame: %w", err)
	}
	return &media.DecodedImage{Frames: []*image.RGBA{downscaleRGBA(first)}}, nil
}

func isGIF(data []byte) bool {
	return len(data) >= 6 && string(data[:3]) == "GIF"
}

// downscaleRGBA converts to RGBA and shrinks to media.MaxImageWidth, so a
// 1920-wide banner does not become a 1920-wide texture on a 960-wide screen.
func downscaleRGBA(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > media.MaxImageWidth {
		h = h * media.MaxImageWidth / w
		w = media.MaxImageWidth
		if h < 1 {
			h = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

// ProcessPending uploads any freshly decoded artwork into SDL textures. It
// MUST be called from the GUI thread, once per frame, before drawing.
// Returns true if anything changed (i.e. a redraw is worthwhile).
func (c *ImageCache) ProcessPending() bool {
	changed := false
	for {
		select {
		case result := <-c.ready:
			c.mu.Lock()
			delete(c.fetching, result.url)
			if result.err != nil || result.image == nil || len(result.image.Frames) == 0 {
				c.failed[result.url] = true
				if result.transient {
					c.retryAt[result.url] = time.Now().Add(90 * time.Second)
				}
				c.mu.Unlock()
				if result.err != nil {
					fmt.Fprintf(os.Stderr, "cover art %s: %v\n", result.url, result.err)
				}
				continue
			}
			frames := result.image.Frames
			// Cap how much of the texture table one animation may hold, so a
			// long GIF cannot evict every still around it.
			if len(frames) > 16 {
				frames = frames[:16]
			}
			slots := make([]int, 0, len(frames))
			for range frames {
				slots = append(slots, c.takeSlotLocked())
			}
			c.mu.Unlock()

			uploadErr := false
			for i, frame := range frames {
				bounds := frame.Bounds()
				pixels := frame.Pix
				if C.bridge_upload_texture(C.int(slots[i]),
					(*C.uchar)(unsafe.Pointer(&pixels[0])),
					C.int(bounds.Dx()), C.int(bounds.Dy())) != 0 {
					uploadErr = true
					break
				}
			}

			c.mu.Lock()
			if uploadErr {
				c.failed[result.url] = true
				c.freeSlot = append(c.freeSlot, slots...)
			} else {
				delays := make([]time.Duration, len(frames))
				for i := range delays {
					if i < len(result.image.Delays) && result.image.Delays[i] > 0 {
						delays[i] = result.image.Delays[i]
					} else {
						delays[i] = media.DefaultFrameDelay
					}
				}
				c.entries[result.url] = &cacheEntry{
					url: result.url, slots: slots, delays: delays,
					started: time.Now(), lastUsed: time.Now(),
				}
				changed = true
			}
			c.mu.Unlock()
		default:
			return changed
		}
	}
}

// takeSlotLocked returns a free texture slot, evicting the least recently
// used entry when the table is full. Caller must hold c.mu.
func (c *ImageCache) takeSlotLocked() int {
	if n := len(c.freeSlot); n > 0 {
		slot := c.freeSlot[n-1]
		c.freeSlot = c.freeSlot[:n-1]
		return slot
	}
	var oldestURL string
	var oldest time.Time
	for url, entry := range c.entries {
		if oldestURL == "" || entry.lastUsed.Before(oldest) {
			oldestURL, oldest = url, entry.lastUsed
		}
	}
	victim := c.entries[oldestURL]
	delete(c.entries, oldestURL)
	slot := victim.slots[0]
	if len(victim.slots) > 1 {
		c.freeSlot = append(c.freeSlot, victim.slots[1:]...)
	}
	return slot
}

// Animating reports whether an animated frame was served since the last call,
// so the draw loop can schedule the next frame only while an animation is
// actually on screen.
//
// Reading CLEARS the flag: a draw sets it again, so it stays true exactly as
// long as an animation remains visible. That makes call order matter — check
// any frame-rate throttle BEFORE calling this, or a throttled tick will
// consume the flag without redrawing and the animation stops after one frame.
func (c *ImageCache) Animating() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.animating
	c.animating = false
	return was
}

// Animating on the Screen, for the main loop.
func (s *Screen) Animating() bool { return s.images.Animating() }

// ProcessPendingImages uploads any artwork that finished downloading and
// reports whether the screen should be redrawn. Call once per frame from the
// GUI thread.
func (s *Screen) ProcessPendingImages() bool {
	return s.images.ProcessPending()
}

// qrSlot is a texture slot reserved outside the LRU: the QR code is tied to
// whichever game page is open, not to the cover-art working set.
const qrSlot = maxTextures

// SetQRCode uploads a QR image (RGBA) into the reserved slot and returns the
// slot, or -1 when it could not be uploaded. Call from the GUI thread.
func (s *Screen) SetQRCode(rgba []byte, w, h int) int {
	if len(rgba) == 0 || w <= 0 || h <= 0 {
		return -1
	}
	if rc := C.bridge_upload_texture(C.int(qrSlot), (*C.uchar)(unsafe.Pointer(&rgba[0])),
		C.int(w), C.int(h)); rc != 0 {
		return -1
	}
	return qrSlot
}

// ClearQRCode frees the reserved slot.
func (s *Screen) ClearQRCode() { C.bridge_destroy_texture(C.int(qrSlot)) }

// Suspend releases the display so another process (RetroArch) can take over
// KMS/DRM. Every texture dies with it, so the cache is emptied too — the
// artwork simply re-downloads on Resume, which costs a few seconds once per
// game launch and keeps this simple and correct.
func (s *Screen) Suspend() {
	C.bridge_release_display()
	s.images.reset()
}

// Resume re-acquires the display after a suspended launch.
func (s *Screen) Resume(fontPath string) error {
	cFont := C.CString(fontPath)
	defer C.free(unsafe.Pointer(cFont))
	if rc := C.bridge_reacquire_display(cFont); rc != 0 {
		return fmt.Errorf("sdlui: could not re-acquire the display (code %d)", rc)
	}
	var w, h C.int
	C.bridge_get_size(&w, &h)
	s.width, s.height = int(w), int(h)
	s.listTop, s.manageTop = 0, 0
	return nil
}

// reset forgets every cached texture. The textures themselves are already
// gone (the renderer that owned them was destroyed); this drops the bookkeeping
// so nothing hands out a stale slot.
func (c *ImageCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.failed = make(map[string]bool)
	c.freeSlot = c.freeSlot[:0]
	for i := 0; i < maxTextures; i++ {
		c.freeSlot = append(c.freeSlot, i)
	}
	c.animating = false
}

// BrowserUserAgent is sent with image requests; some CDNs reject Go's default.
const BrowserUserAgent = "Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// ipv4FirstTransport connects over IPv4 when the host has an IPv4 address.
// On this network the image CDN's IPv6 route keeps resetting connections
// ("connection reset by peer"), while IPv4 works.
func ipv4FirstTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if conn, err := d.DialContext(ctx, "tcp4", addr); err == nil {
			return conn, nil
		}
		return d.DialContext(ctx, network, addr)
	}
	return t
}
