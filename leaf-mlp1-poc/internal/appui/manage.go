package appui

type ManageState uint8

const (
	ManageList ManageState = iota
	ManageConfirm
	ManageResult
	ManageError
)

type ManageItemKind uint8

const (
	ManageItemFile ManageItemKind = iota
	ManageItemDeleteROMs
	ManageItemDeleteMusic
	ManageItemDeleteAll
	ManageItemRename
)

type ManageItem struct {
	Kind                 ManageItemKind
	Label, Detail, Badge string
	FileIndex            int
	Enabled              bool
}

type ManageIntent uint8

const (
	ManageIntentNone ManageIntent = iota
	ManageIntentBack
	ManageIntentActivate
	ManageIntentConfirm
	ManageIntentCancel
)

type ManageModel struct {
	State         ManageState
	Title         string
	Subtitle      string
	Items         []ManageItem
	Cursor        int
	VisibleRows   int
	PromptTitle   string
	PromptLines   []string
	Message       string
	LibraryStatus string
}

func NewManageModel(title string) *ManageModel {
	return &ManageModel{State: ManageList, Title: title, VisibleRows: 1}
}

func (m *ManageModel) SetItems(subtitle string, items []ManageItem) {
	m.State = ManageList
	m.Subtitle = subtitle
	m.Items = append([]ManageItem(nil), items...)
	m.PromptTitle, m.PromptLines, m.Message, m.LibraryStatus = "", nil, "", ""
	if m.Cursor >= len(m.Items) {
		m.Cursor = len(m.Items) - 1
	}
	if m.Cursor < 0 || len(m.Items) == 0 {
		m.Cursor = 0
	}
}

func (m *ManageModel) SetConfirm(title string, lines []string) {
	m.State = ManageConfirm
	m.PromptTitle = title
	m.PromptLines = append([]string(nil), lines...)
	m.Message, m.LibraryStatus = "", ""
}

func (m *ManageModel) SetResult(message string) {
	m.State, m.Message, m.LibraryStatus = ManageResult, message, ""
}

func (m *ManageModel) SetError(message string) {
	m.State, m.Message, m.LibraryStatus = ManageError, message, ""
}

func (m *ManageModel) SetLibraryStatus(status string) { m.LibraryStatus = status }

func (m *ManageModel) Handle(event InputEvent) ManageIntent {
	if !event.Pressed {
		return ManageIntentNone
	}
	if m.State == ManageConfirm {
		switch event.Button {
		case ButtonA:
			return ManageIntentConfirm
		case ButtonB, ButtonQuit:
			return ManageIntentCancel
		}
		return ManageIntentNone
	}
	if m.State == ManageResult || m.State == ManageError {
		if event.Button == ButtonA || event.Button == ButtonB || event.Button == ButtonQuit {
			return ManageIntentBack
		}
		return ManageIntentNone
	}
	if event.Button == ButtonB || event.Button == ButtonQuit {
		return ManageIntentBack
	}
	page := m.VisibleRows
	if page < 1 {
		page = 1
	}
	switch event.Button {
	case ButtonUp:
		m.move(-1)
	case ButtonDown:
		m.move(1)
	case ButtonL1:
		m.move(-page)
	case ButtonR1:
		m.move(page)
	case ButtonA:
		if m.Cursor >= 0 && m.Cursor < len(m.Items) {
			return ManageIntentActivate
		}
	}
	return ManageIntentNone
}

func (m *ManageModel) move(delta int) {
	if len(m.Items) == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor += delta
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= len(m.Items) {
		m.Cursor = len(m.Items) - 1
	}
}
