package appui

type RenameState uint8

const (
	RenameConfirmROM RenameState = iota
	RenameConfirmSaves
	RenameConfirmStates
	RenameDone
	RenameError
)

type RenameIntent uint8

const (
	RenameIntentNone RenameIntent = iota
	RenameIntentConfirm
	RenameIntentSkip
	RenameIntentBack
)

type RenameModel struct {
	State         RenameState
	Title         string
	Subtitle      string
	Heading       string
	Lines         []string
	Message       string
	LibraryStatus string
	ScrollLine    int
	ScrollMax     int
}

func NewRenameModel(title string) *RenameModel { return &RenameModel{Title: title} }

func (m *RenameModel) SetPrompt(state RenameState, subtitle, heading string, lines []string) {
	m.State, m.Subtitle, m.Heading = state, subtitle, heading
	m.Lines = append([]string(nil), lines...)
	m.Message, m.LibraryStatus, m.ScrollLine = "", "", 0
}

func (m *RenameModel) SetDone(message string) {
	m.State, m.Subtitle, m.Message, m.LibraryStatus = RenameDone, "Rename complete", message, ""
}

func (m *RenameModel) SetError(message string) {
	m.State, m.Subtitle, m.Message, m.LibraryStatus = RenameError, "Rename failed", message, ""
}

func (m *RenameModel) SetLibraryStatus(status string) { m.LibraryStatus = status }

func (m *RenameModel) SetScrollBounds(maximum int) {
	if maximum < 0 {
		maximum = 0
	}
	m.ScrollMax = maximum
	if m.ScrollLine > maximum {
		m.ScrollLine = maximum
	}
}

func (m *RenameModel) Handle(event InputEvent) RenameIntent {
	if !event.Pressed {
		return RenameIntentNone
	}
	if m.State == RenameDone || m.State == RenameError {
		if event.Button == ButtonA || event.Button == ButtonB || event.Button == ButtonQuit {
			return RenameIntentBack
		}
		return RenameIntentNone
	}
	switch event.Button {
	case ButtonUp:
		if m.ScrollLine > 0 {
			m.ScrollLine--
		}
	case ButtonDown:
		if m.ScrollLine < m.ScrollMax {
			m.ScrollLine++
		}
	case ButtonA:
		return RenameIntentConfirm
	case ButtonB, ButtonQuit:
		if m.State == RenameConfirmROM {
			return RenameIntentBack
		}
		return RenameIntentSkip
	}
	return RenameIntentNone
}
