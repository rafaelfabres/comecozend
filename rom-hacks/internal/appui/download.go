package appui

type DownloadSelectState uint8

const (
	DownloadSelectLoading DownloadSelectState = iota
	DownloadSelectChoices
	DownloadSelectError
	DownloadSelectHandoff
)

type DownloadChoice struct {
	Title, Detail, Badge string
	FormatOptions        []string
	FormatIndex          int
}

type DownloadSelectIntent uint8

const (
	DownloadSelectIntentNone DownloadSelectIntent = iota
	DownloadSelectIntentBack
	DownloadSelectIntentChoose
)

type DownloadSelectModel struct {
	State       DownloadSelectState
	Title       string
	Subtitle    string
	Choices     []DownloadChoice
	Cursor      int
	VisibleRows int
	Message     string
}

func NewDownloadSelectModel(title string) *DownloadSelectModel {
	return &DownloadSelectModel{State: DownloadSelectLoading, Title: title, VisibleRows: 1}
}

func (m *DownloadSelectModel) SetLoading(subtitle string) {
	m.State = DownloadSelectLoading
	m.Subtitle = subtitle
	m.Message = ""
}

func (m *DownloadSelectModel) SetChoices(subtitle string, choices []DownloadChoice) {
	m.State = DownloadSelectChoices
	m.Subtitle = subtitle
	m.Choices = append([]DownloadChoice(nil), choices...)
	m.Message = ""
	m.clampCursor()
}

func (m *DownloadSelectModel) SetError(message string) {
	m.State = DownloadSelectError
	m.Message = message
}

func (m *DownloadSelectModel) SetHandoff(message string) {
	m.State = DownloadSelectHandoff
	m.Message = message
}

func (m *DownloadSelectModel) Handle(event InputEvent) DownloadSelectIntent {
	if !event.Pressed {
		return DownloadSelectIntentNone
	}
	if event.Button == ButtonB || event.Button == ButtonQuit {
		return DownloadSelectIntentBack
	}
	if m.State != DownloadSelectChoices {
		if m.State == DownloadSelectError && event.Button == ButtonA {
			return DownloadSelectIntentBack
		}
		return DownloadSelectIntentNone
	}
	page := m.VisibleRows
	if page < 1 {
		page = 1
	}
	switch event.Button {
	case ButtonUp:
		m.Cursor--
		m.clampCursor()
	case ButtonDown:
		m.Cursor++
		m.clampCursor()
	case ButtonLeft:
		m.cycleFormat(-1)
	case ButtonRight:
		m.cycleFormat(1)
	case ButtonL1:
		m.Cursor -= page
		m.clampCursor()
	case ButtonR1:
		m.Cursor += page
		m.clampCursor()
	case ButtonA:
		if len(m.Choices) > 0 {
			return DownloadSelectIntentChoose
		}
	}
	return DownloadSelectIntentNone
}

func (m *DownloadSelectModel) cycleFormat(direction int) {
	if m.Cursor < 0 || m.Cursor >= len(m.Choices) {
		return
	}
	choice := &m.Choices[m.Cursor]
	if len(choice.FormatOptions) == 0 {
		return
	}
	choice.FormatIndex = (choice.FormatIndex + direction) % len(choice.FormatOptions)
	if choice.FormatIndex < 0 {
		choice.FormatIndex += len(choice.FormatOptions)
	}
	choice.Badge = choice.FormatOptions[choice.FormatIndex]
}

func (m *DownloadSelectModel) clampCursor() {
	if len(m.Choices) == 0 {
		m.Cursor = 0
		return
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= len(m.Choices) {
		m.Cursor = len(m.Choices) - 1
	}
}

type DownloadProgressState uint8

const (
	DownloadProgressRunning DownloadProgressState = iota
	DownloadProgressDone
	DownloadProgressError
	DownloadProgressInhibitBlocked
	DownloadProgressCancelled
)

type DownloadProgressIntent uint8

const (
	DownloadProgressIntentNone DownloadProgressIntent = iota
	DownloadProgressIntentBack
	DownloadProgressIntentContinue
	DownloadProgressIntentCancel
)

type DownloadProgressModel struct {
	State                   DownloadProgressState
	Title, Filename, Detail string
	Downloaded, Total       int64
	FileIndex, FileCount    int
	SavedPaths              []string
	Locked                  bool // protected operation cannot be cancelled mid-transaction
	LibraryStatus           string
}

func (m *DownloadProgressModel) Handle(event InputEvent) DownloadProgressIntent {
	if !event.Pressed {
		return DownloadProgressIntentNone
	}
	if m.State == DownloadProgressRunning {
		if m.Locked {
			return DownloadProgressIntentNone
		}
		if event.Button == ButtonB || event.Button == ButtonQuit {
			return DownloadProgressIntentCancel
		}
		return DownloadProgressIntentNone
	}
	if m.State == DownloadProgressInhibitBlocked && event.Button == ButtonA {
		return DownloadProgressIntentContinue
	}
	if event.Button == ButtonA || event.Button == ButtonB || event.Button == ButtonQuit {
		return DownloadProgressIntentBack
	}
	return DownloadProgressIntentNone
}
