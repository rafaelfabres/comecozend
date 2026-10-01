package appui

import (
	"strings"

	"leaf-hacks/internal/text"
)

// Button is the renderer-independent control vocabulary used by application
// screens. Platform adapters translate their native input into these values.
type Button uint8

const (
	ButtonNone Button = iota
	ButtonUp
	ButtonDown
	ButtonLeft
	ButtonRight
	ButtonA
	ButtonB
	ButtonX
	ButtonY
	ButtonL1
	ButtonL2
	ButtonR1
	ButtonR2
	ButtonStart
	ButtonSelect
	ButtonQuit
)

type InputEvent struct {
	Button   Button
	Pressed  bool
	Repeated bool
}

type ListState uint8

const (
	ListLoading ListState = iota
	ListReady
	ListEmpty
	ListError
)

type ListItem struct {
	Title, Author, CoverKey, Badge string
	Tags                           []string
}

type ListIntent uint8

const (
	ListIntentNone ListIntent = iota
	ListIntentOpen
	ListIntentExit
	ListIntentRetry
	ListIntentFilter
	ListIntentSettings
	ListIntentPreviousSort
	ListIntentNextSort
	ListIntentPreviousPlatform
	ListIntentNextPlatform
	ListIntentDismissNotice
)

type MainListModel struct {
	State       ListState
	Items       []ListItem
	Cursor      int
	VisibleRows int
	ErrorDetail string
	Platform    string
	Sort        string
	CacheStatus string
}

func NewMainListModel(items []ListItem) *MainListModel {
	state := ListReady
	if len(items) == 0 {
		state = ListEmpty
	}
	return &MainListModel{State: state, Items: items, VisibleRows: 1}
}

func (m *MainListModel) SetLoading() {
	m.State = ListLoading
	m.ErrorDetail = ""
}

func (m *MainListModel) SetError(detail string) {
	m.State = ListError
	m.ErrorDetail = detail
}

func (m *MainListModel) SetItems(items []ListItem) {
	m.Items = items
	m.State = ListReady
	if len(items) == 0 {
		m.State = ListEmpty
	}
	m.clampCursor()
}

func (m *MainListModel) Selected() (ListItem, bool) {
	if m.State != ListReady || m.Cursor < 0 || m.Cursor >= len(m.Items) {
		return ListItem{}, false
	}
	return m.Items[m.Cursor], true
}

func (m *MainListModel) Handle(event InputEvent) ListIntent {
	if !event.Pressed {
		return ListIntentNone
	}
	if event.Button == ButtonQuit {
		return ListIntentExit
	}
	if m.State == ListError {
		switch event.Button {
		case ButtonA:
			return ListIntentRetry
		case ButtonB:
			return ListIntentExit
		default:
			return ListIntentNone
		}
	}
	if m.State == ListLoading {
		if event.Button == ButtonB {
			return ListIntentExit
		}
		return ListIntentNone
	}

	page := m.VisibleRows
	if page < 1 {
		page = 1
	}
	switch event.Button {
	case ButtonUp:
		m.wrapCursor(-1)
	case ButtonDown:
		m.wrapCursor(1)
	case ButtonLeft:
		if m.Sort == "A-Z" || m.Sort == "Z-A" {
			m.Cursor = m.alphaJump(-1)
		} else {
			m.Cursor -= page
		}
		m.clampCursor()
	case ButtonRight:
		if m.Sort == "A-Z" || m.Sort == "Z-A" {
			m.Cursor = m.alphaJump(1)
		} else {
			m.Cursor += page
		}
		m.clampCursor()
	case ButtonA:
		if m.State == ListReady {
			return ListIntentOpen
		}
	case ButtonB:
		return ListIntentExit
	case ButtonX:
		if m.State == ListReady {
			return ListIntentDismissNotice
		}
	case ButtonSelect:
		return ListIntentFilter
	case ButtonStart:
		return ListIntentSettings
	case ButtonL1:
		return ListIntentPreviousSort
	case ButtonR1:
		return ListIntentNextSort
	case ButtonL2:
		return ListIntentPreviousPlatform
	case ButtonR2:
		return ListIntentNextPlatform
	}
	return ListIntentNone
}

func (m *MainListModel) alphaJump(direction int) int {
	if m.Cursor < 0 || m.Cursor >= len(m.Items) {
		return m.Cursor
	}
	current := firstTitleRune(m.Items[m.Cursor].Title)
	for index := m.Cursor + direction; index >= 0 && index < len(m.Items); index += direction {
		if firstTitleRune(m.Items[index].Title) != current {
			return index
		}
	}
	if direction > 0 {
		return len(m.Items) - 1
	}
	return 0
}

func firstTitleRune(title string) rune {
	normalized := strings.ToLower(strings.TrimSpace(text.StripEmoji(title)))
	for _, value := range normalized {
		return value
	}
	return 0
}

// wrapCursor moves the cursor by delta, wrapping top<->bottom like the native
// launcher's cat_list_state_move. Page and alpha jumps stay clamped.
func (m *MainListModel) wrapCursor(delta int) {
	n := len(m.Items)
	if n == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor = ((m.Cursor+delta)%n + n) % n
}

func (m *MainListModel) clampCursor() {
	if len(m.Items) == 0 {
		m.Cursor = 0
		return
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= len(m.Items) {
		m.Cursor = len(m.Items) - 1
	}
}
