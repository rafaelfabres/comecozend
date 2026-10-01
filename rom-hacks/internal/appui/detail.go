package appui

import (
	"strings"

	"golang.org/x/net/html"
)

type DetailState uint8

const (
	DetailLoading DetailState = iota
	DetailReady
	DetailWarning
	DetailError
)

type DetailGame struct {
	Title, Author, URL, Platform    string
	PriceText                       string // overrides the price shown (e.g. "unavailable")
	Price                           float64
	IsFree, Downloaded, CanDownload bool
}

type DetailModel struct {
	State       DetailState
	Game        DetailGame
	Description []string
	Tags        []string
	Images      []string
	ImageIndex  int
	ScrollLine  int
	ScrollMax   int
	BrowserOnly bool
	ErrorDetail string
}

type DetailIntent uint8

const (
	DetailIntentNone DetailIntent = iota
	DetailIntentBack
	DetailIntentSettings
	DetailIntentDownload
	DetailIntentManage
)

func NewDetailModel(game DetailGame) *DetailModel {
	return &DetailModel{State: DetailLoading, Game: game}
}

func (m *DetailModel) SetReady(description string, tags, images []string, browserOnly, warning bool) {
	m.Description = DescriptionParagraphs(description)
	m.Tags = append([]string(nil), tags...)
	m.Images = append([]string(nil), images...)
	m.BrowserOnly = browserOnly
	m.ErrorDetail = ""
	m.State = DetailReady
	if warning {
		m.State = DetailWarning
	}
	m.clampImage()
}

func (m *DetailModel) SetError(detail string) {
	m.State = DetailError
	m.ErrorDetail = detail
}

func (m *DetailModel) SetScrollBounds(maximum int) {
	if maximum < 0 {
		maximum = 0
	}
	m.ScrollMax = maximum
	m.clampScroll()
}

func (m *DetailModel) Handle(event InputEvent) DetailIntent {
	if !event.Pressed {
		return DetailIntentNone
	}
	if event.Button == ButtonB || event.Button == ButtonQuit {
		return DetailIntentBack
	}
	if event.Button == ButtonStart {
		return DetailIntentSettings
	}
	if m.State != DetailReady {
		return DetailIntentNone
	}
	switch event.Button {
	case ButtonLeft, ButtonL1:
		m.ImageIndex--
		m.clampImage()
	case ButtonRight, ButtonR1:
		m.ImageIndex++
		m.clampImage()
	case ButtonUp:
		m.ScrollLine--
		m.clampScroll()
	case ButtonDown:
		m.ScrollLine++
		m.clampScroll()
	case ButtonA:
		if m.Game.CanDownload && !m.BrowserOnly {
			return DetailIntentDownload
		}
	case ButtonX:
		if m.Game.Downloaded {
			return DetailIntentManage
		}
	}
	return DetailIntentNone
}

func (m *DetailModel) clampImage() {
	if len(m.Images) == 0 {
		m.ImageIndex = 0
		return
	}
	if m.ImageIndex < 0 {
		m.ImageIndex = len(m.Images) - 1
	}
	if m.ImageIndex >= len(m.Images) {
		m.ImageIndex = 0
	}
}

func (m *DetailModel) clampScroll() {
	if m.ScrollLine < 0 {
		m.ScrollLine = 0
	}
	if m.ScrollLine > m.ScrollMax {
		m.ScrollLine = m.ScrollMax
	}
}

// DescriptionParagraphs turns the scraper's small HTML subset into readable,
// renderer-independent paragraphs while preserving Unicode text.
func DescriptionParagraphs(markup string) []string {
	doc, err := html.Parse(strings.NewReader("<body>" + markup + "</body>"))
	if err != nil {
		return nil
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style") {
			return
		}
		if node.Type == html.ElementNode && node.Data == "li" {
			out.WriteString("• ")
		}
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		if node.Type == html.ElementNode && node.Data == "br" {
			out.WriteByte('\n')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "p", "div", "h1", "h2", "h3", "li", "ul", "ol":
				out.WriteByte('\n')
			}
		}
	}
	walk(doc)
	lines := strings.Split(out.String(), "\n")
	paragraphs := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			paragraphs = append(paragraphs, line)
		}
	}
	return paragraphs
}
