package appui

type RefreshState uint8

const (
	RefreshLoading RefreshState = iota
	RefreshDone
	RefreshError
	RefreshCancelled
)

type RefreshIntent uint8

const (
	RefreshIntentNone RefreshIntent = iota
	RefreshIntentCancel
	RefreshIntentBack
)

type RefreshModel struct {
	State   RefreshState
	Title   string
	Fetched int
	Total   int
	Detail  string
}

func NewRefreshModel(title string) *RefreshModel {
	return &RefreshModel{State: RefreshLoading, Title: title}
}

func (m *RefreshModel) Handle(event InputEvent) RefreshIntent {
	if !event.Pressed {
		return RefreshIntentNone
	}
	if m.State == RefreshLoading {
		if event.Button == ButtonB || event.Button == ButtonQuit {
			return RefreshIntentCancel
		}
		return RefreshIntentNone
	}
	if event.Button == ButtonA || event.Button == ButtonB || event.Button == ButtonQuit {
		return RefreshIntentBack
	}
	return RefreshIntentNone
}
