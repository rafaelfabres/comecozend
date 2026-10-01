package rahub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ItchBase is overridable for tests.
var ItchBase = "https://itch.io"

// SearchItch runs an itch.io game search and returns the result cells.
// It uses the plain HTML search page (no JavaScript needed). client should
// be the app's browser-fingerprinted itch.io client.
func SearchItch(ctx context.Context, client *http.Client, query string) ([]Candidate, error) {
	u := fmt.Sprintf("%s/search?type=games&q=%s", strings.TrimRight(ItchBase, "/"), url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("itch.io search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itch.io search: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseItchSearch(string(body)), nil
}

// ParseItchSearch extracts game cells from an itch.io search/browse page.
//
// A cell looks like:
//
//	<div class="game_cell ..." data-game_id="123">
//	  <a class="thumb_link game_link" href="URL"><img data-lazy_src="COVER"></a>
//	  <div class="game_title"><a class="title game_link" href="URL">Title</a></div>
//	  <div class="price_value">$2.99</div>
//	  <div class="game_author"><a href="...">Author</a></div>
//	</div>
func ParseItchSearch(page string) []Candidate {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	var out []Candidate
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "game_cell") {
			if c, ok := parseCell(n); ok {
				out = append(out, c)
			}
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return out
}

func parseCell(cell *html.Node) (Candidate, bool) {
	var c Candidate
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "a" && hasClass(n, "title") && hasClass(n, "game_link"):
				c.URL = attr(n, "href")
				c.Title = strings.TrimSpace(textOf(n))
			case n.Data == "a" && c.URL == "" && hasClass(n, "game_link"):
				c.URL = attr(n, "href")
			case n.Data == "img" && c.CoverURL == "":
				if src := attr(n, "data-lazy_src"); src != "" {
					c.CoverURL = src
				} else if src := attr(n, "src"); strings.HasPrefix(src, "http") {
					c.CoverURL = src
				}
			case n.Data == "div" && hasClass(n, "price_value"):
				c.Price = strings.TrimSpace(textOf(n))
			case n.Data == "div" && hasClass(n, "game_author"):
				c.Author = strings.TrimSpace(textOf(n))
			case n.Data == "div" && hasClass(n, "game_text"):
				c.Text = strings.TrimSpace(textOf(n))
				if t := attr(n, "title"); len(t) > len(c.Text) {
					c.Text = t
				}
			case hasClass(n, "web_flag"):
				c.Browser = true
			case n.Data == "span" && hasClass(n, "icon"):
				for _, p := range []string{"windows", "linux", "osx", "android", "apple"} {
					if strings.Contains(attr(n, "class"), "icon-"+p) {
						c.Platforms = append(c.Platforms, p)
					}
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(cell)
	c.Source = "search"
	return c, c.URL != "" && c.Title != ""
}

func hasClass(n *html.Node, class string) bool {
	for _, f := range strings.Fields(attr(n, "class")) {
		if f == class {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// FetchItchProfile lists the games on a developer's itch.io page
// (https://<slug>.itch.io/), which uses the same game_cell grid as search.
//
// movedTo is set when the page has no games but links to another itch.io
// account ("Health Potion Studios is now Distracted Coder. Check out that
// account: https://distractedcoder.itch.io/").
func FetchItchProfile(ctx context.Context, client *http.Client, slug string) (games []Candidate, movedTo string, err error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || strings.ContainsAny(slug, "/:?# ") {
		return nil, "", fmt.Errorf("bad profile name %q", slug)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+slug+".itch.io/", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("profile %s: HTTP %d", slug, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	out := ParseItchSearch(string(body))
	for i := range out {
		out[i].Source = "profile"
		if out[i].Author == "" {
			out[i].Author = slug
		}
	}
	if len(out) == 0 {
		movedTo = MovedProfile(string(body), slug)
	}
	return out, movedTo, nil
}

var profileLinkRe = regexp.MustCompile(`https?://([a-z0-9][a-z0-9_-]*)\.itch\.io/?["'<\s]`)

// MovedProfile finds, on an empty developer page, a link to another
// itch.io account (the developer's new name).
func MovedProfile(body, slug string) string {
	for _, m := range profileLinkRe.FindAllStringSubmatch(strings.ToLower(body), -1) {
		user := m[1]
		switch user {
		case slug, "www", "itch", "static", "img", "blog", "help", "status":
			continue
		}
		return user
	}
	return ""
}

// ProfileSlugs guesses itch.io subdomains from developer names:
// "Nova32" → "nova32", "Pixel Russ" → "pixelruss" and "pixel-russ".
func ProfileSlugs(developer string) []string {
	loose := strings.Fields(NormLoose(developer))
	if len(loose) == 0 {
		return nil
	}
	joined := strings.Join(loose, "")
	out := []string{joined}
	if len(loose) > 1 {
		out = append(out, strings.Join(loose, "-"))
		// "Zeichi Gameplay Short" is often just "zeichi" on itch.io, or
		// "zeichigames" (studio-style names).
		if len(loose[0]) >= 4 {
			out = append(out, loose[0], loose[0]+"games", loose[0]+"game", loose[0]+"dev")
		}
	}
	return out
}

// SearchQueries returns the searches to try for a hub game, most specific
// first: title + each developer hint, then the title alone, then title +
// console name (short generic titles need the console to find the right
// page among unrelated PC/browser games).
func SearchQueries(g HubGame, developers []string) []string {
	clean := CleanTitle(g.Title)
	var qs []string
	for _, d := range developers {
		qs = append(qs, clean+" "+d)
	}
	qs = append(qs, clean)
	if main := MainTitle(clean); main != clean {
		qs = append(qs, main)
	}
	c := g.Console()
	qs = append(qs, clean+" "+c.Name)
	return qs
}
