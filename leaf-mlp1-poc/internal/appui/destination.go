package appui

type DestinationPhase uint8

const (
	DestinationSources DestinationPhase = iota
	DestinationFolders
	DestinationConfirm
	DestinationError
)

type DestinationItemKind uint8

const (
	DestinationItemSource DestinationItemKind = iota
	DestinationItemSave
	DestinationItemUp
	DestinationItemFolder
)

type DestinationItem struct {
	Kind                 DestinationItemKind
	Label, Detail, Value string
	Enabled              bool
}

type DestinationIntent uint8

const (
	DestinationIntentNone DestinationIntent = iota
	DestinationIntentActivate
	DestinationIntentBack
)

type DestinationModel struct {
	Phase        DestinationPhase
	Title        string
	Subtitle     string
	Path         string
	Items        []DestinationItem
	Cursor       int
	VisibleRows  int
	ErrorDetail  string
	SummaryLines []string
}

func NewDestinationModel(title string) *DestinationModel {
	return &DestinationModel{Title: title, Phase: DestinationSources, VisibleRows: 1}
}

func (m *DestinationModel) SetSources(items []DestinationItem) {
	m.Phase = DestinationSources
	m.Subtitle = "Choose storage card"
	m.Path = ""
	m.Items = append([]DestinationItem(nil), items...)
	m.SummaryLines = nil
	m.Cursor = firstEnabledDestination(items)
	m.ErrorDetail = ""
}

func (m *DestinationModel) SetFolders(subtitle, path string, items []DestinationItem) {
	m.Phase = DestinationFolders
	m.Subtitle, m.Path = subtitle, path
	m.Items = append([]DestinationItem(nil), items...)
	m.SummaryLines = nil
	m.Cursor = 0
	m.ErrorDetail = ""
}

func (m *DestinationModel) SetConfirm(subtitle, path string, lines []string) {
	m.Phase = DestinationConfirm
	m.Subtitle, m.Path = subtitle, path
	m.Items = nil
	m.SummaryLines = append([]string(nil), lines...)
	m.Cursor = 0
	m.ErrorDetail = ""
}

func (m *DestinationModel) SetError(detail string) {
	m.Phase = DestinationError
	m.ErrorDetail = detail
}

func (m *DestinationModel) Handle(event InputEvent) DestinationIntent {
	if !event.Pressed {
		return DestinationIntentNone
	}
	if event.Button == ButtonB || event.Button == ButtonQuit {
		return DestinationIntentBack
	}
	if m.Phase == DestinationError {
		if event.Button == ButtonA {
			return DestinationIntentBack
		}
		return DestinationIntentNone
	}
	if m.Phase == DestinationConfirm {
		if event.Button == ButtonA {
			return DestinationIntentActivate
		}
		return DestinationIntentNone
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
		if m.Cursor >= 0 && m.Cursor < len(m.Items) && m.Items[m.Cursor].Enabled {
			return DestinationIntentActivate
		}
	}
	return DestinationIntentNone
}

func (m *DestinationModel) move(delta int) {
	if len(m.Items) == 0 {
		m.Cursor = 0
		return
	}
	index := m.Cursor
	step := 1
	if delta < 0 {
		step = -1
	}
	remaining := delta
	if remaining < 0 {
		remaining = -remaining
	}
	for remaining > 0 {
		next := index + step
		if next < 0 || next >= len(m.Items) {
			break
		}
		index = next
		if m.Items[index].Enabled {
			remaining--
		}
	}
	if m.Items[index].Enabled {
		m.Cursor = index
	}
}

func firstEnabledDestination(items []DestinationItem) int {
	for index, item := range items {
		if item.Enabled {
			return index
		}
	}
	return 0
}
