package appui

type SettingsState uint8

const (
	SettingsList SettingsState = iota
	SettingsConfirm
	SettingsMessage
	SettingsError
	SettingsWorking
)

type SettingsKey uint16

const (
	SettingsAPIKey SettingsKey = iota
	SettingsEditAPIKey
	SettingsRemoveAPIKey
	SettingsROMSelection
	SettingsROMLocation
	SettingsMusicDownload
	SettingsMusicLocation
	SettingsUnifiedNaming
	SettingsLogLevel
	SettingsROMDestination
	SettingsMusicDestination
	SettingsResetDestinations
	SettingsAppData
	SettingsClearImages
	SettingsRefreshGames
	SettingsUpdateInventory
	SettingsContentModeration
	SettingsAbout
	SettingsAdultContent
	SettingsQueerContent
	SettingsHeavyThemes
	SettingsSubstanceUse
	SettingsTagMaster
	SettingsTag
)

type SettingsRow struct {
	Key           SettingsKey
	Label, Value  string
	Index         int
	ActionEnabled bool
}

type SettingsIntent uint8

const (
	SettingsIntentNone SettingsIntent = iota
	SettingsIntentBack
	SettingsIntentActivate
	SettingsIntentConfirm
	SettingsIntentCancel
)

type SettingsModel struct {
	State       SettingsState
	Title       string
	Subtitle    string
	Rows        []SettingsRow
	Cursor      int
	VisibleRows int
	PromptTitle string
	PromptLines []string
	Message     string
}

func NewSettingsModel(title string) *SettingsModel {
	return &SettingsModel{State: SettingsList, Title: title, VisibleRows: 1}
}

func (m *SettingsModel) SetRows(subtitle string, rows []SettingsRow) {
	m.State, m.Subtitle = SettingsList, subtitle
	m.Rows = append([]SettingsRow(nil), rows...)
	m.PromptTitle, m.PromptLines, m.Message = "", nil, ""
	if len(m.Rows) == 0 {
		m.Cursor = 0
	} else if m.Cursor >= len(m.Rows) {
		m.Cursor = len(m.Rows) - 1
	} else if m.Cursor < 0 {
		m.Cursor = 0
	}
}

func (m *SettingsModel) Selected() (SettingsRow, bool) {
	if m.State != SettingsList || m.Cursor < 0 || m.Cursor >= len(m.Rows) {
		return SettingsRow{}, false
	}
	return m.Rows[m.Cursor], true
}

func (m *SettingsModel) SetConfirm(title string, lines []string) {
	m.State, m.PromptTitle = SettingsConfirm, title
	m.PromptLines = append([]string(nil), lines...)
}

func (m *SettingsModel) SetMessage(message string) {
	m.State, m.Message = SettingsMessage, message
}

func (m *SettingsModel) SetError(message string) {
	m.State, m.Message = SettingsError, message
}

func (m *SettingsModel) Handle(event InputEvent) SettingsIntent {
	if !event.Pressed {
		return SettingsIntentNone
	}
	if m.State == SettingsConfirm {
		switch event.Button {
		case ButtonA:
			return SettingsIntentConfirm
		case ButtonB, ButtonQuit:
			return SettingsIntentCancel
		}
		return SettingsIntentNone
	}
	if m.State == SettingsWorking {
		if event.Button == ButtonB || event.Button == ButtonQuit {
			return SettingsIntentBack
		}
		return SettingsIntentNone
	}
	if m.State == SettingsMessage || m.State == SettingsError {
		if event.Button == ButtonA || event.Button == ButtonB || event.Button == ButtonQuit {
			return SettingsIntentBack
		}
		return SettingsIntentNone
	}
	if event.Button == ButtonB || event.Button == ButtonQuit {
		return SettingsIntentBack
	}
	page := m.VisibleRows
	if page < 1 {
		page = 1
	}
	switch event.Button {
	case ButtonUp:
		m.Cursor--
	case ButtonDown:
		m.Cursor++
	case ButtonL1:
		m.Cursor -= page
	case ButtonR1:
		m.Cursor += page
	case ButtonA:
		if row, ok := m.Selected(); ok && row.ActionEnabled {
			return SettingsIntentActivate
		}
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if len(m.Rows) > 0 && m.Cursor >= len(m.Rows) {
		m.Cursor = len(m.Rows) - 1
	}
	return SettingsIntentNone
}
