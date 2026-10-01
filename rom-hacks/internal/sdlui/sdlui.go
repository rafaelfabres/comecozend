// Package sdlui is the drop-in replacement for Leaf's internal/catui: it
// renders appui models with plain SDL2 instead of the closed-source
// Catastrophe library, so it can be built on any Debian-based system (like
// dArkOS) that already ships SDL2 for EmulationStation.
package sdlui

/*
#cgo pkg-config: sdl2 SDL2_ttf
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unsafe"

	"leaf-hacks/internal/appui"
)

// Screen owns the SDL2 window for the lifetime of the process, same role
// catui.Context played for the Catastrophe renderer.
type Screen struct {
	width, height int
	// backdropOK: the dimmed list behind modals is cached and still valid;
	// capturing: the list is being drawn into that cache right now.
	backdropOK, capturing bool
	settingsTop           int // first row shown on settings-style screens
	loadingCatalog        bool
	// previewInfo carries per-game detail for the side panel.
	previewInfo  PreviewInfo
	filterActive bool
	// accountLabel is the signed-in indicator shown in the list header.
	accountLabel string
	// images owns cover-art textures; see imagecache.go.
	images *ImageCache
	// manageTop is the manage screen's own scroll position.
	manageTop int
	// listTop is the index of the first visible list row. It is persistent
	// state, not recomputed from the cursor each frame: recomputing is what
	// made the highlight stick to the bottom row when scrolling back up,
	// since it always solved for "cursor on the last visible line".
	listTop int
}

// Size reports the actual window size in pixels.
func (s *Screen) Size() (int, int) { return s.width, s.height }

// ListRows is how many list rows fit on screen, derived from the real
// window height rather than a hardcoded guess.
func (s *Screen) ListRows() int {
	usable := s.height - listHeaderHeight - footerHeight
	rows := usable / rowHeight
	if rows < 1 {
		rows = 1
	}
	return rows
}

// Open initializes SDL2 + SDL2_ttf and creates the window. fontPath must
// point at a TTF file (the real pak ships its own under assets/font.ttf).
func Open(fontPath string, width, height int) (*Screen, error) {
	cFont := C.CString(fontPath)
	defer C.free(unsafe.Pointer(cFont))
	if rc := C.bridge_init(cFont, C.int(width), C.int(height)); rc != 0 {
		return nil, errors.New("sdlui: bridge_init failed, see stderr for SDL_GetError()")
	}
	var w, h C.int
	C.bridge_get_size(&w, &h)
	return &Screen{width: int(w), height: int(h), images: NewImageCache()}, nil
}

func (s *Screen) Close() {
	C.bridge_shutdown()
}

// Poll blocks briefly waiting for input and returns the equivalent
// appui.InputEvent, or ok=false on timeout (caller should just loop again).
// This is the entire translation table that used to live in
// catui/main_list_screen.go's appButton() — everything else about input
// handling is unchanged, because appui.MainListModel.Handle() doesn't care
// who produced the event.
func (s *Screen) Poll() (event appui.InputEvent, ok bool, quit bool) {
	var raw C.BridgeInput
	var pressedC C.int
	if C.bridge_poll(&raw, &pressedC) == 0 {
		return appui.InputEvent{}, false, false
	}
	pressed := pressedC != 0
	if raw.quit != 0 {
		return appui.InputEvent{}, true, true
	}
	button := buttonFrom(raw)
	if button == appui.ButtonNone {
		return appui.InputEvent{}, false, false
	}
	return appui.InputEvent{Button: button, Pressed: pressed}, true, false
}

func buttonFrom(raw C.BridgeInput) appui.Button {
	switch {
	case raw.up != 0:
		return appui.ButtonUp
	case raw.down != 0:
		return appui.ButtonDown
	case raw.left != 0:
		return appui.ButtonLeft
	case raw.right != 0:
		return appui.ButtonRight
	case raw.a != 0:
		return appui.ButtonA
	case raw.b != 0:
		return appui.ButtonB
	case raw.x != 0:
		return appui.ButtonX
	case raw.y != 0:
		return appui.ButtonY
	case raw.l1 != 0:
		return appui.ButtonL1
	case raw.r1 != 0:
		return appui.ButtonR1
	case raw.l2 != 0:
		return appui.ButtonL2
	case raw.r2 != 0:
		return appui.ButtonR2
	case raw.start != 0:
		return appui.ButtonStart
	case raw.sel != 0:
		return appui.ButtonSelect
	default:
		return appui.ButtonNone
	}
}

const rowHeight = 56
const listHeaderHeight = 32
const footerHeight = 36
const manageHeaderHeight = 72
const filterSlotBase = 64
const keyboardSlotBase = 80
const tagSlotBase = 57
const tagSlotMax = 63

// DrawMainList is the direct equivalent of catui.MainListScreen.Draw(): it
// reads the renderer-independent model and paints it. This is the only
// function a port to a different toolkit (curses, a different SDL wrapper,
// whatever dArkOS ends up wanting) would need to rewrite per screen.
// listSplitPercent mirrors Leaf's own ListDetailSplit(content, 58, ...) in
// internal/catui/main_list_screen.go: the list gets 58% of the width and the
// remainder is a preview card for the selected game.
const listSplitPercent = 58

func (s *Screen) DrawMainList(model *appui.MainListModel, installed []bool) {
	s.DrawMainListInto(model, installed)
	C.bridge_present()
}

// DrawMainListInto paints the list without presenting, so another layer (the
// filter modal) can be composited on top of it.
// installed is parallel to model.Items and marks games already on disk.
// SetLoadingCatalog records whether a catalog fetch is actually in flight, so
// the empty state can tell "still downloading" from "this filter matches
// nothing" — CacheStatus alone cannot, since it also carries the cache age.
func (s *Screen) SetLoadingCatalog(loading bool) { s.loadingCatalog = loading }

// SetFilterActive records whether a search/platform filter is applied, so the
// empty state can offer to clear it.
func (s *Screen) SetFilterActive(active bool) { s.filterActive = active }

func (s *Screen) DrawMainListInto(model *appui.MainListModel, installed []bool) {
	if !s.capturing {
		s.backdropOK = false // the list changed on screen: the cached backdrop is stale
	}
	C.bridge_begin_frame()

	selected, hasSelection := model.Selected()

	switch model.State {
	case appui.ListLoading:
		s.text(0, 24, 24, "Loading...", true)
	case appui.ListError:
		s.text(0, 24, 24, "Error: "+model.ErrorDetail, true)
		s.text(1, 24, 56, "A: Retry   B: Exit", false)
	case appui.ListEmpty:
		// Distinguish "the catalog is still downloading" from "this filter
		// matches nothing" — they look identical otherwise, and the first
		// one resolves on its own.
		if s.loadingCatalog {
			s.text(0, 24, 40, "Still loading the catalog...", true)
			s.text(1, 24, 76, "Nothing matches yet. More games are still arriving.", false)
		} else if s.filterActive {
			s.text(0, 24, 40, "No games match this filter.", true)
			s.text(1, 24, 76, "A filter is hiding everything in the catalog.", false)
			// An explicit way out, since the filter that emptied the list
			// may have been set several screens ago.
			label := "Y  -  Clear filter and show all games"
			cLabel := C.CString(label)
			labelW := int(C.bridge_measure_small(cLabel))
			C.free(unsafe.Pointer(cLabel))
			C.bridge_fill_rect(24, 116, C.int(labelW+36), 34, 56, 56, 64, 255)
			C.bridge_fill_rect(24, 116, 3, 34, 88, 150, 240, 255)
			s.text(2, 42, 123, label, false)
		} else {
			s.text(0, 24, 40, "The catalog is empty.", true)
			s.text(1, 24, 76, "START: Settings  >  refresh, or check the network.", false)
		}
	case appui.ListReady:
		total := len(model.Items)
		visible := model.VisibleRows
		if visible < 1 {
			visible = 1
		}
		// Slide the window only as far as needed to keep the cursor inside
		// it. Because s.listTop persists between frames, the highlight moves
		// freely within the visible rows and the list only scrolls once the
		// cursor would leave them — in both directions.
		if maxTop := total - visible; s.listTop > maxTop {
			s.listTop = maxTop
		}
		if s.listTop < 0 {
			s.listTop = 0
		}
		if model.Cursor < s.listTop {
			s.listTop = model.Cursor
		}
		if model.Cursor >= s.listTop+visible {
			s.listTop = model.Cursor - visible + 1
		}
		top := s.listTop

		platformLabel := model.Platform
		if platformLabel == "" {
			platformLabel = "All Systems"
		}
		// Header band: app name in the accent colour, then where you are.
		C.bridge_fill_rect(0, 3, C.int(s.width), C.int(listHeaderHeight-3), 30, 30, 38, 255)
		C.bridge_fill_rect(0, C.int(listHeaderHeight-1), C.int(s.width), 1, 88, 150, 240, 110)
		// App name as a pill, measured with the font it is drawn in (the
		// big font measured as small text overlapped "All Systems").
		cApp := C.CString("RA Hack")
		appW := int(C.bridge_draw_pill(C.int(15), 12, 5, 22, cApp))
		C.free(unsafe.Pointer(cApp))
		position := fmt.Sprintf("%s   %d/%d", platformLabel, model.Cursor+1, total)
		if s.filterActive {
			position += "      filtered (Y to clear)"
		}
		if model.CacheStatus != "" {
			position += "      " + model.CacheStatus
		}
		// Keep clear of the account name on the right.
		maxW := s.width - (12 + appW + 12) - 24
		if s.accountLabel != "" {
			cA := C.CString(s.accountLabel)
			maxW -= int(C.bridge_measure_small(cA)) + 40
			C.free(unsafe.Pointer(cA))
		}
		s.text(3, 12+appW+12, 4, fitText(position, maxW), false)

		// Signed-in indicator, right-aligned in the header strip.
		if s.accountLabel != "" {
			cAcct := C.CString(s.accountLabel)
			acctW := int(C.bridge_measure_small(cAcct))
			C.free(unsafe.Pointer(cAcct))
			dotX := s.width - acctW - 34
			C.bridge_fill_rect(C.int(dotX), 10, 8, 8, 90, 190, 110, 255)
			s.text(6, s.width-acctW-18, 4, s.accountLabel, false)
		}

		listWidth := s.width * listSplitPercent / 100
		for row := 0; row < visible && top+row < total; row++ {
			i := top + row
			item := model.Items[i]
			y := listHeaderHeight + row*rowHeight
			cTitle := C.CString(item.Title)
			cAuthor := C.CString(item.Author)
			cBadge := C.CString(item.Badge)
			sel := 0
			if i == model.Cursor {
				sel = 1
			}
			isInstalled := 0
			if i < len(installed) && installed[i] {
				isInstalled = 1
			}
			C.bridge_draw_row_w(C.int(row), C.int(y), C.int(rowHeight), C.int(listWidth),
				cTitle, cAuthor, cBadge, C.int(sel), C.int(isInstalled))
			C.free(unsafe.Pointer(cTitle))
			C.free(unsafe.Pointer(cAuthor))
			C.free(unsafe.Pointer(cBadge))
		}

		if hasSelection {
			s.drawPreviewPanel(selected, listWidth, s.previewInfo)
			// Prefetch neighbours so moving the cursor doesn't stall on a
			// cold download — same idea as Leaf warming the next carousel
			// image in detail_screen.go.
			if model.Cursor+1 < total {
				s.images.Warm(model.Items[model.Cursor+1].CoverKey)
			}
			if model.Cursor > 0 {
				s.images.Warm(model.Items[model.Cursor-1].CoverKey)
			}
		}
	}

	s.footer("A: Open   SELECT: Filter   START: Settings   X: Downloads   Y: Clear filter   L2/R2: System   L1/R1: Page   B: Exit")
}

// drawPreviewPanel renders the selected game's card to the right of the list:
// cover art on top, then title/author/price — Leaf's DrawPreviewCard layout,
// where the art takes 58% of the panel height.
// previewInfo is the extra per-game detail the panel shows under the art.
type PreviewInfo struct {
	System    string
	Price     string
	Published string
	Tags      string
	Installed string // file size / status when the game is on disk
	Adult     bool
	// Description is the game page's full text when it has been fetched;
	// empty means fall back to the short RSS blurb carried on the item.
	Description string
}

func (s *Screen) drawPreviewPanel(item appui.ListItem, listWidth int, info PreviewInfo) {
	const pad = 16
	panelX := listWidth + pad
	panelY := listHeaderHeight + pad
	panelW := s.width - panelX - pad
	panelH := s.height - footerHeight - panelY - pad
	if panelW <= 0 || panelH <= 0 {
		return
	}

	C.bridge_fill_rect(C.int(panelX), C.int(panelY), C.int(panelW), C.int(panelH), 32, 32, 38, 235)
	C.bridge_fill_rect(C.int(panelX), C.int(panelY), C.int(panelW), 2, 88, 150, 240, 200)

	inner := pad
	artX, artY := panelX+inner, panelY+inner
	artW := panelW - inner*2
	// Big artwork, as in Leaf. The facts under it are a single line now, so
	// the description still gets the rest of the panel.
	artH := panelH * 52 / 100

	switch {
	case item.CoverKey == "":
		s.text(4, artX, artY+artH/2, "No image", false)
	default:
		slot := s.images.Slot(item.CoverKey)
		switch {
		case slot >= 0:
			C.bridge_draw_texture_fit(C.int(slot), C.int(artX), C.int(artY), C.int(artW), C.int(artH), 255)
		case s.images.Failed(item.CoverKey):
			s.text(4, artX, artY+artH/2, "No image", false)
		default:
			s.text(4, artX, artY+artH/2, "Loading artwork...", false)
		}
	}

	y := artY + artH + 12
	bottom := panelY + panelH - inner

	// Title: measure what it actually drew and advance past it. A fixed
	// advance meant a three-line title printed straight over the metadata.
	titleMaxH := 3 * 30
	cTitle := C.CString(item.Title)
	titleLines := int(C.bridge_draw_paragraph_large_n(C.int(artX), C.int(y), C.int(artW),
		C.int(titleMaxH), cTitle))
	C.free(unsafe.Pointer(cTitle))
	if titleLines < 1 {
		titleLines = 1
	}
	y += titleLines*30 + 6

	// Facts line: system, price, and install state — the things you decide on.
	facts := info.System
	if info.Price != "" {
		facts = joinFact(facts, info.Price)
	}
	if info.Installed != "" {
		facts = joinFact(facts, info.Installed)
	}
	if info.Adult {
		facts = joinFact(facts, "18+")
	}
	// Wrapped, not a single line: "Game Boy Advance - Free - Installed -
	// 13.2 MB" is longer than the panel and was running off the screen edge.
	if facts != "" && y+22 < bottom {
		cFacts := C.CString(facts)
		lines := int(C.bridge_draw_paragraph_scroll(C.int(artX), C.int(y), C.int(artW), 48, cFacts, 0))
		C.free(unsafe.Pointer(cFacts))
		if lines < 1 {
			lines = 1
		}
		if lines > 2 {
			lines = 2
		}
		y += lines*22 + 4
	}
	if info.Published != "" && y+22 < bottom {
		s.text(6, artX, y, info.Published, false)
		y += 26
	}
	if info.Tags != "" && y+22 < bottom {
		cTags := C.CString(info.Tags)
		C.bridge_draw_paragraph_scroll(C.int(artX), C.int(y), C.int(artW), 24, cTags, 0)
		C.free(unsafe.Pointer(cTags))
		y += 28
	}

	// Whatever vertical space is left goes to the description, which is the
	// reason the art block was shrunk from 58% to 38% of the panel.
	body := info.Description
	if strings.TrimSpace(body) == "" {
		body = item.Author // the short RSS blurb
	}
	if body != "" && bottom-y > 20 {
		cBlurb := C.CString(body)
		C.bridge_draw_paragraph_scroll(C.int(artX), C.int(y), C.int(artW), C.int(bottom-y), cBlurb, 0)
		C.free(unsafe.Pointer(cBlurb))
	}
}

func joinFact(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "   -   " + add
}

// text draws via a fixed cache slot (0-7) — pick a slot that's unique among
// the other text() calls made by the same screen within one frame, so each
// on-screen label gets its own cache entry instead of colliding.
func (s *Screen) text(slot, x, y int, value string, emphasized bool) {
	cText := C.CString(value)
	emph := 0
	if emphasized {
		emph = 1
	}
	C.bridge_draw_text(C.int(slot), C.int(x), C.int(y), cText, C.int(emph))
	C.free(unsafe.Pointer(cText))
}

// DrawDetail is catui/detail_screen.go's counterpart: reads appui.DetailModel
// and paints title, price/author line, description and a footer. Screenshot
// carousel and scroll clamping exist in the model already (ImageIndex,
// ScrollLine) but aren't rendered here — out of scope for this PoC.
// DrawDetail lays the selected game out full-screen: its art as a dimmed
// backdrop, a crisp copy on the left, and title/author/price/tags/status plus
// the description on the right.
// DrawDetail follows Leaf's own detail layout (internal/catui/detail_screen.go
// drawReady): a 60/40 split with the screenshot gallery on the left and, on
// the right, a QR code to open the page on a phone, the tags as chips, and the
// description under an "About" heading. Every region has a fallback string, so
// a sparse game page leaves no blank column.
func (s *Screen) DrawDetail(model *appui.DetailModel, status, coverKey string, files []string, qrSlot int) {
	C.bridge_begin_frame()

	// Title lives in the header, as it does in Leaf's ScreenSpec.
	s.text(0, 20, 6, model.Game.Title, true)
	subtitle := model.Game.Author
	price := "Free"
	switch {
	case model.Game.PriceText != "":
		price = model.Game.PriceText
	case !model.Game.IsFree && model.Game.Price < 0:
		price = "price: checking..."
	case !model.Game.IsFree:
		price = PriceFormat(model.Game.Price)
	}
	subtitle += "   ·   " + price
	if model.Game.Platform != "" {
		subtitle += "   ·   " + model.Game.Platform
	}
	if model.Game.Downloaded && !strings.Contains(strings.ToLower(model.Game.PriceText), "installed") {
		subtitle += "   ·   Installed"
	}
	s.text(1, 20, 44, subtitle, false)

	const pad = 18
	top := 78
	bodyH := s.height - footerHeight - top - pad

	switch model.State {
	case appui.DetailLoading:
		s.text(2, 20, top+20, "Loading game details...", false)
		s.text(3, 20, top+52, "Reading screenshots, tags and download metadata.", false)
	case appui.DetailError:
		s.text(2, 20, top+20, "Could not load details", true)
		s.text(3, 20, top+52, model.ErrorDetail, false)
	default:
		galleryW := s.width*60/100 - pad
		galleryX := pad

		// --- left: screenshot gallery, falling back to the cover art ---
		imageKey, label := "", ""
		switch {
		case len(model.Images) > 0:
			idx := model.ImageIndex % len(model.Images)
			imageKey = model.Images[idx]
			label = fmt.Sprintf("Image %d/%d   (L1/R1)", idx+1, len(model.Images))
			if len(model.Images) > 1 {
				s.images.Warm(model.Images[(idx+1)%len(model.Images)])
			}
		case coverKey != "":
			imageKey, label = coverKey, "Cover art"
		}

		labelH := 26
		artH := bodyH - labelH
		if imageKey != "" && s.images.Failed(imageKey) && coverKey != "" && !s.images.Failed(coverKey) {
			// Some itch.io screenshots are long animated GIFs that the
			// decoder rejects. Showing the cover beats showing an error.
			imageKey = coverKey
			label = "Cover art (screenshot unavailable)"
		}
		if imageKey == "" {
			s.text(2, galleryX, top+artH/2, "No screenshots on this page.", false)
		} else if slot := s.images.Slot(imageKey); slot >= 0 {
			C.bridge_draw_texture_fit(C.int(slot), C.int(galleryX), C.int(top), C.int(galleryW), C.int(artH), 255)
		} else if s.images.Failed(imageKey) {
			s.text(2, galleryX, top+artH/2, "Image could not be decoded.", false)
		} else {
			s.text(2, galleryX, top+artH/2, "Loading image...", false)
		}
		if label != "" {
			s.text(3, galleryX, top+artH+4, label, false)
		}

		// --- right: QR, tags, files, About ---
		panelX := s.width*60/100 + pad
		panelW := s.width - panelX - pad
		y := top

		// Status card: what can be done with this game, colour-coded
		// (green ok, blue ready, amber attention, red unavailable).
		if DetailStatus.Text != "" {
			r, g, bl := 50, 90, 160
			switch DetailStatus.Level {
			case StatusOK:
				r, g, bl = 45, 130, 75
			case StatusWarn:
				r, g, bl = 150, 105, 25
			case StatusBad:
				r, g, bl = 160, 45, 45
			}
			cSt := C.CString(DetailStatus.Text)
			lines := int(C.bridge_paragraph_lines(C.int(panelW-24), cSt))
			if lines > 4 {
				lines = 4
			}
			cardH := lines*22 + 16
			C.bridge_fill_rect(C.int(panelX), C.int(y), C.int(panelW), C.int(cardH), C.int(r), C.int(g), C.int(bl), 255)
			C.bridge_fill_rect(C.int(panelX), C.int(y), 4, C.int(cardH), 255, 255, 255, 120)
			C.bridge_draw_card_text(0, C.int(panelX+12), C.int(y+8), C.int(panelW-24), C.int(cardH-12), cSt)
			C.free(unsafe.Pointer(cSt))
			y += cardH + 10
		}

		if model.Game.Downloaded {
			play := "Y  -  PLAY"
			cPlay := C.CString(play)
			playW := int(C.bridge_measure_small(cPlay))
			C.free(unsafe.Pointer(cPlay))
			C.bridge_fill_rect(C.int(panelX), C.int(y), C.int(playW+36), 32, 70, 150, 90, 255)
			// Bright, not dimmed: grey-on-green read as a disabled button.
			cPlayLabel := C.CString(play)
			C.bridge_draw_text_bright(9, C.int(panelX+18), C.int(y+5), cPlayLabel)
			C.free(unsafe.Pointer(cPlayLabel))
			y += 42
		}
		// The last action's message (download progress, errors), when it
		// says something the status card does not.
		if status != "" && status != DetailStatus.Text {
			cStatus := C.CString(status)
			lines := int(C.bridge_paragraph_lines(C.int(panelW-16), cStatus))
			if lines > 3 {
				lines = 3
			}
			bannerH := lines*22 + 10
			C.bridge_fill_rect(C.int(panelX), C.int(y), C.int(panelW), C.int(bannerH), 60, 60, 72, 255)
			C.bridge_draw_card_text(1, C.int(panelX+8), C.int(y+5), C.int(panelW-16), C.int(bannerH-10), cStatus)
			C.free(unsafe.Pointer(cStatus))
			y += bannerH + 10
		}

		// Key facts as label / value rows.
		if len(DetailFacts) > 0 {
			labelW := 0
			for _, f := range DetailFacts {
				cL := C.CString(f[0])
				if w := int(C.bridge_measure_small(cL)); w > labelW {
					labelW = w
				}
				C.free(unsafe.Pointer(cL))
			}
			labelW += 14
			for i, f := range DetailFacts {
				if y > top+bodyH-120 {
					break
				}
				cL := C.CString(f[0])
				C.bridge_draw_text(C.int(20+(i%8)), C.int(panelX), C.int(y), cL, 0)
				C.free(unsafe.Pointer(cL))
				cV := C.CString(fitText(f[1], panelW-labelW-8))
				C.bridge_draw_text_bright(C.int(28+(i%8)), C.int(panelX+labelW), C.int(y), cV)
				C.free(unsafe.Pointer(cV))
				y += 24
			}
			y += 8
		}

		// Tags as chips, flowed across the panel width.
		if len(model.Tags) > 0 {
			chipX, chipSlot := panelX, tagSlotBase
			for _, tag := range model.Tags {
				cTag := C.CString(tag)
				w := int(C.bridge_measure_small(cTag)) + 20
				if chipX+w > panelX+panelW {
					break // one row of itch.io tags is enough
				}
				if y > top+bodyH-100 {
					C.free(unsafe.Pointer(cTag))
					break
				}
				C.bridge_draw_pill(C.int(chipSlot), C.int(chipX), C.int(y), 26, cTag)
				C.free(unsafe.Pointer(cTag))
				chipX += w + 6
				chipSlot++
				if chipSlot > tagSlotMax {
					break
				}
			}
			y += 38
		}

		if len(files) > 0 {
			s.text(6, panelX, y, "Files: "+strings.Join(files, ", "), false)
			y += 30
		}

		s.text(7, panelX, y, "About", true)
		y += 28
		body := strings.Join(model.Description, "\n")
		if strings.TrimSpace(body) == "" {
			body = "No description was provided."
		}
		// Clip the description to the space actually left, and report how many
		// lines it wanted so the model can bound scrolling — the same
		// arrangement Leaf uses (DrawScrollingBody + SetScrollBounds).
		// Reserve a strip at the bottom for the scroll indicator so it never
		// lands on top of the last line of text.
		const indicatorH = 26
		availH := top + bodyH - y - indicatorH
		if availH < 22 {
			availH = 22
		}
		cBody := C.CString(body)
		totalLines := int(C.bridge_draw_paragraph_scroll(C.int(panelX), C.int(y),
			C.int(panelW), C.int(availH), cBody, C.int(model.ScrollLine)))
		C.free(unsafe.Pointer(cBody))
		visibleLines := availH / 22
		if over := totalLines - visibleLines; over > 0 {
			model.SetScrollBounds(over)
			s.text(8, panelX, top+bodyH-18,
				fmt.Sprintf("L2/R2 to scroll  -  %d/%d", model.ScrollLine+1, over+1), false)
		} else {
			model.SetScrollBounds(0)
		}
	}

	// Only the actions: D-pad/L/R navigation is not listed, it made the
	// row too long for the screen.
	chips := [][2]string{{"X", "Downloads"}, {"B", "Back"}}
	if model.State == appui.DetailReady {
		switch {
		case model.BrowserOnly:
			chips = [][2]string{{"X", "Downloads"}, {"B", "Back"}}
		case model.Game.Downloaded:
			chips = [][2]string{{"Y", "Play"}, {"A", DetailReinstallLabel}, {"X", "Downloads"}, {"B", "Back"}}
		default:
			chips = [][2]string{{"A", "Download"}}
			if DetailNoDownload {
				chips = nil // a demo that no longer exists
			}
			if DetailHintNotInstalled != "" {
				chips = append(chips, [2]string{"Y", DetailHintNotInstalled})
			}
			if DetailHintExtra != "" {
				chips = append(chips, [2]string{"SELECT", DetailHintExtra})
			}
			if DetailFullGame {
				chips = append(chips, [2]string{"X", "Full game"})
			} else {
				chips = append(chips, [2]string{"X", "Downloads"})
			}
			chips = append(chips, [2]string{"B", "Back"})
		}
	}
	s.footerChips(chips)
	C.bridge_present()
}

// footerChips draws the footer as [button] label pairs.
func (s *Screen) footerChips(chips [][2]string) {
	var b strings.Builder
	for i, c := range chips {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c[0] + "\t" + c[1])
	}
	cs := C.CString(b.String())
	C.bridge_draw_footer_chips(cs)
	C.free(unsafe.Pointer(cs))
}

func (s *Screen) key(slot, x, y, w, h int, label string, selected bool) {
	cLabel := C.CString(label)
	sel := 0
	if selected {
		sel = 1
	}
	C.bridge_draw_key(C.int(slot), C.int(x), C.int(y), C.int(w), C.int(h), cLabel, C.int(sel))
	C.free(unsafe.Pointer(cLabel))
}

// DrawFilter renders appui.FilterModel's three rows (search query, platform,
// sort) as selectable boxes. Left/Right cycle platform/sort in place; A on
// the search row hands off to the on-screen keyboard (DrawKeyboard) since
// there's no physical keyboard and Catastrophe's native text entry has no
// equivalent in this vendored code.
// DrawFilter renders the filter as a centred modal over the dimmed list,
// rather than a separate full screen. Leaf uses its own screen here, but on a
// 960x720 handheld a popup keeps the list visible for context and makes the
// staged-until-applied behaviour obvious.
//
// Row style follows Leaf's DrawValueRow: label on the left, value on the
// right, with < > markers on the rows that cycle.
func (s *Screen) DrawFilter(model *appui.FilterModel, behind *appui.MainListModel, installed []bool) {
	s.modalBackground(behind, installed, 190)

	modalW := s.width * 70 / 100
	modalH := 402
	modalX := (s.width - modalW) / 2
	modalY := (s.height - modalH) / 2

	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), C.int(modalH), 34, 34, 40, 250)
	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), 3, 88, 150, 240, 255)

	pad := 20
	s.text(0, modalX+pad, modalY+pad, "Filter & Search", true)
	s.text(1, modalX+pad, modalY+pad+30, "Changes apply on SELECT", false)

	query := model.Query
	if query == "" {
		query = "Any title, author or system"
	}
	rows := []struct {
		label, value string
		cycles       bool
	}{
		{"Search", query, false},
		{"System", model.PlatformLabel(), true},
		{"Genre", model.GenreLabel(), true},
		{"Order", model.SortLabel(), true},
		{"Show", model.ShowLabel(), true},
	}
	rowH := 46
	rowY := modalY + pad + 66
	for i, row := range rows {
		y := rowY + i*rowH
		selected := int(model.Section) == i
		if selected {
			C.bridge_fill_rect(C.int(modalX+pad-8), C.int(y-4), C.int(modalW-pad*2+16), C.int(rowH-6),
				50, 90, 160, 255)
			C.bridge_fill_rect(C.int(modalX+pad-8), C.int(y-4), 3, C.int(rowH-6), 88, 150, 240, 255)
		}
		s.text(2+i, modalX+pad, y+8, row.label, false)

		value := row.value
		if row.cycles {
			value = "< " + value + " >"
		}
		cVal := C.CString(value)
		valW := int(C.bridge_measure_small(cVal))
		C.free(unsafe.Pointer(cVal))
		s.text(5+i, modalX+modalW-pad-valW, y+8, value, false)
	}

	// An explicit Apply button inside the modal: a footer hint alone was not
	// making it obvious which button commits the staged filter.
	// A quiet outlined chip rather than a solid red slab: the accent colour
	// reads as an alert at that size, which is not what an Apply button is.
	apply := "SELECT  ·  Apply filter"
	cApply := C.CString(apply)
	applyW := int(C.bridge_measure_small(cApply))
	C.free(unsafe.Pointer(cApply))
	btnH := 34
	btnW := applyW + 36
	btnX := modalX + (modalW-btnW)/2
	btnY := modalY + modalH - btnH - 18
	C.bridge_fill_rect(C.int(btnX), C.int(btnY), C.int(btnW), C.int(btnH), 56, 56, 64, 255)
	C.bridge_fill_rect(C.int(btnX), C.int(btnY), 3, C.int(btnH), 88, 150, 240, 255)
	s.text(4, btnX+18, btnY+7, apply, false)

	s.footer("Left/Right: Change   A: Edit   Y: Clear   B: Cancel")
	C.bridge_present()
}

// DrawKeyboard renders a simple on-screen keyboard grid — there's no
// vendored equivalent to reuse here (Catastrophe's text entry is native and
// closed-source), so this is new, purpose-built UI chrome rather than a
// port of an existing appui model.
// DrawKeyboard renders the on-screen keyboard as a modal panel. There is no
// vendored equivalent to port — Catastrophe's text entry is native and
// closed-source — so this is purpose-built UI chrome.
func (s *Screen) DrawKeyboard(query string, rows [][]string, curRow, curCol int, prompt string) {
	C.bridge_begin_frame()
	C.bridge_fill_rect(0, 0, C.int(s.width), C.int(s.height), 8, 8, 10, 190)

	const keyW, keyH, gap = 74, 52, 8
	widest := 0
	for _, row := range rows {
		w := 0
		for _, label := range row {
			if len(label) > 3 {
				w += keyW*2 + gap*2
			} else {
				w += keyW + gap
			}
		}
		if w > widest {
			widest = w
		}
	}
	pad := 22
	modalW := widest + pad*2
	if modalW > s.width-20 {
		modalW = s.width - 20
	}
	modalH := len(rows)*(keyH+gap) + pad*2 + 64
	modalX := (s.width - modalW) / 2
	modalY := (s.height - modalH) / 2

	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), C.int(modalH), 34, 34, 40, 250)
	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), 3, 88, 150, 240, 255)

	// Query box, so it is obvious what is being typed.
	boxY := modalY + pad
	C.bridge_fill_rect(C.int(modalX+pad), C.int(boxY), C.int(modalW-pad*2), 38, 20, 20, 24, 255)
	shown := query
	if shown == "" {
		shown = prompt
	}
	s.text(0, modalX+pad+10, boxY+8, shown, true)

	startY := boxY + 54
	slot := keyboardSlotBase
	for r, row := range rows {
		x := modalX + pad
		y := startY + r*(keyH+gap)
		for c, label := range row {
			w := keyW
			if len(label) > 3 {
				w = keyW*2 + gap
			}
			s.key(slot, x, y, w, keyH, label, r == curRow && c == curCol)
			slot++
			x += w + gap
		}
	}

	s.footer("A: Type   Y: Backspace   START: Done   B: Cancel")
	C.bridge_present()
}

// DrawManage renders appui.ManageModel: the list of installed ROMs, plus its
// confirm / result / error prompts.
// DrawManage paints the downloads manager as a modal over the list, matching
// the filter and keyboard popups.
func (s *Screen) DrawManage(model *appui.ManageModel, behind *appui.MainListModel, installed []bool) {
	s.modalBackground(behind, installed, 195)

	modalW := s.width * 80 / 100
	modalH := s.height * 78 / 100
	modalX := (s.width - modalW) / 2
	modalY := (s.height - modalH) / 2
	pad := 20

	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), C.int(modalH), 34, 34, 40, 250)
	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), 3, 88, 150, 240, 255)

	s.text(0, modalX+pad, modalY+pad, model.Title, true)

	switch model.State {
	case appui.ManageConfirm:
		s.text(1, modalX+pad, modalY+pad+44, model.PromptTitle, true)
		cLines := C.CString(strings.Join(model.PromptLines, "  "))
		C.bridge_draw_paragraph(C.int(modalX+pad), C.int(modalY+pad+78), C.int(modalW-pad*2), cLines)
		C.free(unsafe.Pointer(cLines))
		s.footer("A: Delete   B: Cancel")
	case appui.ManageResult, appui.ManageError:
		cMsg := C.CString(model.Message)
		C.bridge_draw_paragraph(C.int(modalX+pad), C.int(modalY+pad+44), C.int(modalW-pad*2), cMsg)
		C.free(unsafe.Pointer(cMsg))
		s.footer("A / B: Back")
	default:
		s.text(1, modalX+pad, modalY+pad+30, model.Subtitle, false)

		listY := modalY + pad + 64
		listH := modalH - (listY - modalY) - pad
		visible := listH / rowHeight
		if visible < 1 {
			visible = 1
		}
		model.VisibleRows = visible

		total := len(model.Items)
		if maxTop := total - visible; s.manageTop > maxTop {
			s.manageTop = maxTop
		}
		if s.manageTop < 0 {
			s.manageTop = 0
		}
		if model.Cursor < s.manageTop {
			s.manageTop = model.Cursor
		}
		if model.Cursor >= s.manageTop+visible {
			s.manageTop = model.Cursor - visible + 1
		}

		for row := 0; row < visible && s.manageTop+row < total; row++ {
			i := s.manageTop + row
			item := model.Items[i]
			y := listY + row*rowHeight
			selected := i == model.Cursor
			if selected {
				C.bridge_fill_rect(C.int(modalX+pad-8), C.int(y), C.int(modalW-pad*2+16),
					C.int(rowHeight-4), 50, 90, 160, 255)
				C.bridge_fill_rect(C.int(modalX+pad-8), C.int(y), 3, C.int(rowHeight-4), 88, 150, 240, 255)
			}
			base := row * 3
			cLabel := C.CString(item.Label)
			cDetail := C.CString(item.Detail)
			cBadge := C.CString(item.Badge)
			C.bridge_draw_manage_row(C.int(base), C.int(modalX+pad), C.int(y), C.int(rowHeight),
				C.int(modalW-pad*2), cLabel, cDetail, cBadge)
			C.free(unsafe.Pointer(cLabel))
			C.free(unsafe.Pointer(cDetail))
			C.free(unsafe.Pointer(cBadge))
		}
		if total == 0 {
			s.text(2, modalX+pad, listY+10, "Games you download here will be listed in this window.", false)
		}
		s.footer("Y: Play   A: Delete   X: Open   B: Close")
	}
	C.bridge_present()
}

// footer draws a hint line in the same button style as the game page:
// "A: Open   B: Back" becomes [A] Open  [B] Back. Items are separated by
// two or more spaces; each is "BUTTON: what it does".
func (s *Screen) footer(text string) {
	var chips [][2]string
	for _, item := range footerSplit.Split(strings.TrimSpace(text), -1) {
		key, label, ok := strings.Cut(item, ":")
		if !ok {
			chips = append(chips, [2]string{"", strings.TrimSpace(item)})
			continue
		}
		chips = append(chips, [2]string{strings.TrimSpace(key), strings.TrimSpace(label)})
	}
	s.footerChips(chips)
}

var footerSplit = regexp.MustCompile(`\s{2,}`)

// ManageRows is how many rows the manage screen can show.
func (s *Screen) ManageRows() int {
	usable := s.height - manageHeaderHeight - footerHeight
	rows := usable / rowHeight
	if rows < 1 {
		rows = 1
	}
	return rows
}

// DrawConfirm paints a small centred confirmation modal over whatever was
// drawn behind it. The caller is responsible for painting that background
// first (via one of the *Into helpers) so the dialog reads as a layer.
func (s *Screen) DrawConfirm(title, body, hint string) {
	C.bridge_fill_rect(0, 0, C.int(s.width), C.int(s.height), 8, 8, 10, 200)

	modalW := s.width * 55 / 100
	modalH := 180
	modalX := (s.width - modalW) / 2
	modalY := (s.height - modalH) / 2

	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), C.int(modalH), 34, 34, 40, 252)
	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), 3, 88, 150, 240, 255)

	pad := 22
	s.text(0, modalX+pad, modalY+pad, title, true)
	cBody := C.CString(body)
	C.bridge_draw_paragraph(C.int(modalX+pad), C.int(modalY+pad+40), C.int(modalW-pad*2), cBody)
	C.free(unsafe.Pointer(cBody))
	s.text(1, modalX+pad, modalY+modalH-40, hint, false)
	C.bridge_present()
}

// DrawQuitConfirm shows the exit dialog over the list.
func (s *Screen) DrawQuitConfirm(behind *appui.MainListModel, installed []bool) {
	s.modalBackground(behind, installed, 0)
	s.DrawConfirm("Exit RA Hack?", "Installed games stay on the SD card.",
		"A: Exit      B: Stay")
}

// settingsRow is one line of the settings popup.
type SettingsRow struct {
	Label, Value string
	// Section starts a new group with this heading above the row.
	Section string
	// Action rows read as buttons rather than as a value being displayed.
	Action bool
}

// DrawSettings renders the settings popup. It follows Leaf's own settings
// screen (internal/ui/cat_settings_flow.go): label on the left, current value
// on the right, with the API key masked rather than shown.
// DrawSettings is a full screen, not a popup: there are enough rows that a
// modal either scrolled or squeezed the values against the labels.
func (s *Screen) DrawSettings(rows []SettingsRow, cursor int, behind *appui.MainListModel, installed []bool) {
	C.bridge_begin_frame()

	pad := 32
	rowH, headingH := 46, 34

	// Header band with the accent rule, matching the rest of the app.
	C.bridge_fill_rect(0, 3, C.int(s.width), 58, 40, 40, 48, 255)
	s.text(0, pad, 18, "Settings", true)
	if s.accountLabel != "" {
		cAcct := C.CString(s.accountLabel)
		acctW := int(C.bridge_measure_small(cAcct))
		C.free(unsafe.Pointer(cAcct))
		C.bridge_fill_rect(C.int(s.width-acctW-pad-18), 28, 8, 8, 90, 190, 110, 255)
		s.text(1, s.width-acctW-pad, 22, s.accountLabel, false)
	}

	y := 78
	slot := 2
	valueX := s.width / 2
	// Scroll so the selected row is always on screen (Settings, Hidden
	// systems and Hidden games can be longer than the screen). The top row
	// is kept between frames and moved only as far as needed, measuring
	// the real height of every row and section heading, so each press of
	// Down moves the view by exactly one row once the bottom is reached.
	height := func(i int) int {
		if rows[i].Section != "" {
			return rowH + headingH
		}
		return rowH
	}
	avail := s.height - footerHeight - y
	if cursor < s.settingsTop || s.settingsTop >= len(rows) {
		s.settingsTop = cursor
	}
	for {
		used := 0
		for i := s.settingsTop; i <= cursor && i < len(rows); i++ {
			used += height(i)
		}
		if used <= avail || s.settingsTop >= cursor {
			break
		}
		s.settingsTop++
	}
	if s.settingsTop < 0 {
		s.settingsTop = 0
	}
	start := s.settingsTop
	for i, row := range rows {
		if i < start {
			continue
		}
		// Check space for the heading AND its first row together. Drawing
		// the heading first and only then testing for room produced a
		// section title ("STORAGE") with nothing under it.
		needed := rowH
		if row.Section != "" {
			needed += headingH
		}
		if y+needed > s.height-footerHeight {
			break
		}
		if row.Section != "" {
			s.text(slot%8, pad, y+6, row.Section, false)
			C.bridge_fill_rect(C.int(pad), C.int(y+headingH-8), C.int(s.width-pad*2), 1, 88, 150, 240, 90)
			slot++
			y += headingH
		}

		selected := i == cursor
		if selected {
			C.bridge_fill_rect(C.int(pad-12), C.int(y), C.int(s.width-pad*2+24), C.int(rowH-6),
				50, 90, 160, 255)
			C.bridge_fill_rect(C.int(pad-12), C.int(y), 4, C.int(rowH-6), 88, 150, 240, 255)
		}

		label := row.Label
		if row.Action && selected {
			label = "> " + label
		}
		cLabel := C.CString(label)
		C.bridge_draw_text_bright(C.int(slot%8+8), C.int(pad), C.int(y+10), cLabel)
		C.free(unsafe.Pointer(cLabel))
		slot++

		// Values are wrapped at half-width so a long path does not run off
		// the right edge.
		if row.Value != "" {
			cVal := C.CString(row.Value)
			C.bridge_draw_paragraph_scroll(C.int(valueX), C.int(y+10), C.int(s.width-valueX-pad),
				C.int(rowH-10), cVal, 0)
			C.free(unsafe.Pointer(cVal))
		}
		y += rowH
	}

	s.footer("A: Select   B / START: Back")
	C.bridge_present()
}

// SetAccountLabel sets (or clears, with "") the signed-in indicator.
func (s *Screen) SetAccountLabel(label string) { s.accountLabel = label }

// SetPreviewInfo supplies the extra detail shown for the selected game.
func (s *Screen) SetPreviewInfo(info PreviewInfo) { s.previewInfo = info }

// PriceFormat writes a price for the detail header (the app sets reais).
var PriceFormat = func(v float64) string { return fmt.Sprintf("$%.2f", v) }

// DetailHintExtra is appended to the detail screen's footer hint.
var DetailHintExtra = ""

// DetailHintNotInstalled is appended to the footer of a game that is not
// installed (Y is free there).
var DetailHintNotInstalled = ""

// modalBackground draws the dimmed list behind a modal. The list is
// rendered once into a cached texture (see bridge_backdrop_begin) and
// reused until the list is drawn normally again.
func (s *Screen) modalBackground(behind *appui.MainListModel, installed []bool, dim uint8) {
	if behind == nil {
		C.bridge_begin_frame()
		return
	}
	if !s.backdropOK {
		if C.bridge_backdrop_begin() == 1 {
			s.capturing = true
			s.DrawMainListInto(behind, installed)
			C.bridge_fill_rect(0, 0, C.int(s.width), C.int(s.height), 8, 8, 10, C.int(dim))
			s.capturing = false
			C.bridge_backdrop_end()
			s.backdropOK = true
		} else {
			// No render-target support: draw it directly, as before.
			s.DrawMainListInto(behind, installed)
			C.bridge_fill_rect(0, 0, C.int(s.width), C.int(s.height), 8, 8, 10, C.int(dim))
			return
		}
	}
	C.bridge_begin_frame()
	C.bridge_draw_backdrop()
}

// DetailFullGame: the game page's X installs the purchased full game
// (demo games whose full release the user bought).
var DetailFullGame bool

// DetailNoDownload hides "A Download" (a demo that is no longer offered).
var DetailNoDownload bool

// QRLabel is the caption under the game page's QR code.
var QRLabel = "Scan to open on itch.io"

// Status card levels for the game page.
const (
	StatusReady = iota
	StatusOK
	StatusWarn
	StatusBad
)

// DetailStatus is the game page's status card (set by the app per game).
var DetailStatus struct {
	Text  string
	Level int
}

// DetailFacts are the game page's label/value rows.
var DetailFacts [][2]string

// fitText shortens text with "..." until it fits maxW pixels (small font).
func fitText(text string, maxW int) string {
	measure := func(t string) int {
		c := C.CString(t)
		defer C.free(unsafe.Pointer(c))
		return int(C.bridge_measure_small(c))
	}
	if maxW <= 0 || measure(text) <= maxW {
		return text
	}
	r := []rune(text)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if measure(string(r)+"...") <= maxW {
			break
		}
	}
	return string(r) + "..."
}

// DetailReinstallLabel names A on an installed game ("Reinstall", "Update").
var DetailReinstallLabel = "Reinstall"

// DrawInfo paints a large scrollable panel over the current screen, for
// text too long for the game page's own About box — a hack's readme runs
// to hundreds of lines and is the only place most of them say what they
// change.
//
// Returns the number of lines the text wanted and how many fit, so the
// caller can bound scrolling.
func (s *Screen) DrawInfo(title, subtitle, body string, scroll int, hint string) (total, visible int) {
	C.bridge_fill_rect(0, 0, C.int(s.width), C.int(s.height), 8, 8, 10, 215)

	modalW := s.width * 82 / 100
	modalH := s.height * 82 / 100
	modalX := (s.width - modalW) / 2
	modalY := (s.height - modalH) / 2

	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), C.int(modalH), 34, 34, 40, 252)
	C.bridge_fill_rect(C.int(modalX), C.int(modalY), C.int(modalW), 3, 88, 150, 240, 255)

	pad := 22
	y := modalY + pad
	s.text(0, modalX+pad, y, title, true)
	y += 30
	if subtitle != "" {
		s.text(2, modalX+pad, y, subtitle, false)
		y += 26
	}
	y += 6

	const indicatorH = 26
	availH := modalY + modalH - y - pad - indicatorH
	if availH < 22 {
		availH = 22
	}
	cBody := C.CString(body)
	total = int(C.bridge_draw_paragraph_scroll(C.int(modalX+pad), C.int(y),
		C.int(modalW-pad*2), C.int(availH), cBody, C.int(scroll)))
	C.free(unsafe.Pointer(cBody))
	visible = availH / 22

	if total > visible {
		s.text(3, modalX+pad, modalY+modalH-pad-18,
			fmt.Sprintf("%s  -  %d/%d", hint, scroll+1, total-visible+1), false)
	} else if hint != "" {
		s.text(3, modalX+pad, modalY+modalH-pad-18, hint, false)
	}
	C.bridge_present()
	return total, visible
}
