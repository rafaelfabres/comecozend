package rahub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SearchVersion is bumped whenever the lookup gets new ways to find a page.
// Games recorded as NOT_FOUND by an older version are looked up again.
const SearchVersion = 15 // 15: titles that differ only in spacing ("Ring Dash" / "RingDash")

// itchGameURL matches an itch.io game page: https://<user>.itch.io/<game>.
var itchGameURL = regexp.MustCompile(`https?://([a-z0-9][a-z0-9_-]*)\.itch\.io/([a-z0-9][a-z0-9_-]*)/?(?:[?#"'&<\s]|$)`)

// notGamePaths are itch.io paths that are never a game page.
var notGamePaths = map[string]bool{"devlog": true, "devlogs": true, "rss": true, "feed": true, "community": true,
	"download": true, "purchase": true, "embed": true, "jobs": true, "games": true}

// ExtractItchGameURLs finds itch.io game page URLs in any text (a search
// engine's result page, including DuckDuckGo's "uddg=" redirect links).
func ExtractItchGameURLs(page string) []string {
	// Decode redirect wrappers first so the real URL is visible.
	text := page
	for _, m := range regexp.MustCompile(`uddg=([^&"'\s]+)`).FindAllStringSubmatch(page, -1) {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			text += " " + u + " "
		}
	}
	// Bing wraps results as .../ck/a?...&u=a1<base64url of the target>.
	for _, m := range regexp.MustCompile(`[?&;]u=a1([A-Za-z0-9_-]+)`).FindAllStringSubmatch(page, -1) {
		if b, err := base64.RawURLEncoding.DecodeString(m[1]); err == nil {
			text += " " + string(b) + " "
		}
	}
	// Yahoo wraps results as .../RU=<escaped target>/RK=...
	for _, m := range regexp.MustCompile(`/RU=([^/"'\s]+)/R[KS]=`).FindAllStringSubmatch(page, -1) {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			text += " " + u + " "
		}
	}
	// Google wraps results as /url?q=<escaped target>&...
	for _, m := range regexp.MustCompile(`/url\?q=([^&"'\s]+)`).FindAllStringSubmatch(page, -1) {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			text += " " + u + " "
		}
	}
	text = html.UnescapeString(text)
	seen := map[string]bool{}
	var out []string
	for _, m := range itchGameURL.FindAllStringSubmatch(text+" ", -1) {
		user, game := strings.ToLower(m[1]), strings.ToLower(m[2])
		if user == "www" || user == "itch" || user == "static" || user == "img" || notGamePaths[game] {
			continue
		}
		u := "https://" + user + ".itch.io/" + game
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// SlugTitle turns "night-watch-at-wilfords" into "Night Watch At Wilfords".
func SlugTitle(slug string) string {
	words := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// TitleSlugs returns likely itch.io URL slugs for a title
// ("Night Watch At Wilford's" → "night-watch-at-wilfords").
func TitleSlugs(title string) []string {
	clean := strings.ToLower(CleanTitle(title))
	clean = strings.NewReplacer("'", "", "’", "", "&", " and ").Replace(clean)
	var b strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) == 0 {
		return nil
	}
	out := []string{strings.Join(words, "-")}
	if len(words) > 1 {
		out = append(out, strings.Join(words, ""))
	}
	// Two words of the title written as one, as developers often name
	// their games: RA's "Ring Dash GBA" is brig78cx.itch.io/ringdash-gba.
	if len(words) > 2 && len(words) <= 5 {
		for i := 0; i+1 < len(words); i++ {
			joined := append(append(append([]string(nil), words[:i]...), words[i]+words[i+1]), words[i+2:]...)
			out = append(out, strings.Join(joined, "-"))
		}
	}
	return out
}

// JoinedTitles returns the title with two neighbouring words written as
// one — "Ring Dash GBA" → "RingDash GBA", "Ring DashGBA" — for searches.
// itch.io's search, like the title comparison, works on whole words, so a
// game published as "RingDash" is not found by "Ring Dash". Only short
// titles: past five words a joined pair is a guess too far.
func JoinedTitles(clean string) []string {
	words := strings.Fields(clean)
	if len(words) < 2 || len(words) > 5 {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(words); i++ {
		joined := append(append(append([]string(nil), words[:i]...), words[i]+words[i+1]), words[i+2:]...)
		out = append(out, strings.Join(joined, " "))
	}
	return out
}

// WebLog, when set, receives one line per search engine tried, so a
// failing web search can be diagnosed from the log (engines block bots).
var WebLog func(format string, args ...any)

func webLog(format string, args ...any) {
	if WebLog != nil {
		WebLog(format, args...)
	}
}

// webEngines are plain-HTML result pages that work without JavaScript, in
// the order tried. Queries go as a person would type them, without "site:".
// Order from what answered on the device: Bing, DuckDuckGo and Yahoo return
// links; Google and Startpage came back empty, Brave rate-limits (429) and
// Mojeek refuses (403), so those come last or not at all.
// Google answers a text browser (Lynx) with a plain HTML result page; to a
// Chrome-looking client without JavaScript it returns nothing usable.
var webEngines = []struct{ name, url, ua string }{
	{"bing", "https://www.bing.com/search?q=", ""},
	{"google", "https://www.google.com/search?hl=en&gbv=1&q=", "Lynx/2.9.0dev.12 libwww-FM/2.14 SSL-MM/1.4.1 GNUTLS/3.7.8"},
	{"duckduckgo", "https://html.duckduckgo.com/html/?q=", ""},
	{"yahoo", "https://search.yahoo.com/search?p=", ""},
	{"startpage", "https://www.startpage.com/do/search?q=", ""},
	{"brave", "https://search.brave.com/search?q=", ""},
}

// plainClient is used for engines that want a simple (non-Chrome) client.
var plainClient = &http.Client{Timeout: 30 * time.Second}

// WebSearchItch asks web search engines for itch.io pages. itch.io's own
// search does not return every game, while a web search finds any public
// page. For each query, engines are tried in turn; it stops as soon as a
// result satisfies good (a title or developer match). An engine that
// refuses (403/429/202 challenge) is not asked again in this call.
// Everything collected is returned, most relevant engines' results first.
//
// devFirst says queries[0] is the "title + developer" query.
func WebSearchItch(ctx context.Context, client *http.Client, queries []string, devFirst bool, good func(Candidate) bool) ([]Candidate, error) {
	dead := map[string]bool{}
	seen := map[string]bool{}
	var out []Candidate
	var lastErr error
	requests := 0
	for _, query := range queries {
		q := url.QueryEscape(query + " itch.io")
		for _, e := range webEngines {
			if dead[e.name] || requests >= 10 {
				continue
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if requests > 0 {
				if err := sleepCtx(ctx, 800*time.Millisecond); err != nil {
					return out, err
				}
			}
			requests++
			var body string
			var err error
			if e.ua != "" {
				body, err = getTextUA(ctx, plainClient, e.url+q, e.ua)
			} else {
				body, err = getText(ctx, client, e.url+q)
			}
			if err != nil {
				webLog("        web search %s: %v", e.name, err)
				dead[e.name] = true
				lastErr = err
				continue
			}
			urls := ExtractItchGameURLs(body)
			hit := false
			for rank, gu := range urls {
				if seen[gu] {
					continue
				}
				seen[gu] = true
				if hit {
					continue // a page already qualified; do not open more
				}
				slug := gu[strings.LastIndex(gu, "/")+1:]
				user := strings.TrimPrefix(gu[:strings.Index(gu, ".itch.io")], "https://")
				c := Candidate{URL: gu, Title: SlugTitle(slug), Author: user, Source: "web",
					WebRank: rank + 1, DevQuery: query == queries[0] && devFirst}
				if good != nil && good(c) {
					hit = true
				}
				out = append(out, c)
			}
			webLog("        web search %s %q: %d itch.io link(s)%s", e.name, query, len(urls), map[bool]string{true: ", match", false: ""}[hit])
			if hit {
				return out, nil
			}
		}
	}
	if len(out) == 0 && lastErr == nil {
		lastErr = fmt.Errorf("no search engine returned an itch.io link")
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, lastErr
}

var (
	ogTitleRe   = regexp.MustCompile(`<meta[^>]+property="og:title"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+property="og:title"`)
	ogImageRe   = regexp.MustCompile(`<meta[^>]+property="og:image"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+property="og:image"`)
	priceMetaRe = regexp.MustCompile(`itemprop="price"[^>]*content="([0-9.]+)"|content="([0-9.]+)"[^>]*itemprop="price"`)
	dollarsRe   = regexp.MustCompile(`class="dollars[^"]*"[^>]*>\s*([^<]+?)\s*<`)
	authorRe    = regexp.MustCompile(`<meta[^>]+name="author"[^>]+content="([^"]*)"`)
	ogDescRe    = regexp.MustCompile(`<meta[^>]+property="og:description"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+property="og:description"`)
	descDivRe   = regexp.MustCompile(`<div class="formatted_description[^"]*"[^>]*>`)
	tagRe       = regexp.MustCompile(`(?s)<[^>]*>`)
	paraRe      = regexp.MustCompile(`(?is)<p(?:\s[^>]*)?>(.*?)</p>`)
	scriptRe    = regexp.MustCompile(`(?is)<(script|style|nav|header|footer)[^>]*>.*?</(script|style|nav|header|footer)>`)
	twImageRe   = regexp.MustCompile(`<meta[^>]+name="twitter:image"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+name="twitter:image"`)
	twTitleRe   = regexp.MustCompile(`<meta[^>]+name="twitter:title"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+name="twitter:title"`)
	htmlTitleRe = regexp.MustCompile(`(?is)<title[^>]*>([^<]+)</title>`)
	twDescRe    = regexp.MustCompile(`<meta[^>]+name="twitter:description"[^>]+content="([^"]*)"|<meta[^>]+content="([^"]*)"[^>]+name="twitter:description"`)
	// Upload file names in the page's download list.
	uploadNameRe = regexp.MustCompile(`<strong[^>]+class="name"[^>]*>([^<]+)</strong>|class="upload_name"[^>]*>\s*<strong[^>]*title="([^"]+)"`)
)

// ParseGamePage reads title, cover, author and price from an itch.io game
// page's HTML (Open Graph and schema.org tags).
func ParseGamePage(pageURL, body string) (Candidate, bool) {
	first := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(body); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					return html.UnescapeString(g)
				}
			}
		}
		return ""
	}
	// itch.io pages do not always carry og:title (some only have
	// twitter:title and <title>).
	title := first(ogTitleRe)
	if title == "" {
		title = first(twTitleRe)
	}
	if title == "" {
		title = strings.TrimSpace(first(htmlTitleRe))
	}
	if title == "" {
		return Candidate{}, false
	}
	c := Candidate{URL: pageURL, Title: title, CoverURL: SmallCover(first(ogImageRe)), Author: first(authorRe), Source: "page"}
	c.Links = sameAccountLinks(pageURL, body)
	// The page text: used when the itch.io title differs from RA's.
	c.Text = first(ogDescRe) + " " + first(twDescRe)
	// Upload names often carry the name RA uses ("KOTZ - The Phoenix
	// Returns v1.3.0E ENGLISH.gbc" on the "Saint Seiya ..." page).
	for _, m := range uploadNameRe.FindAllStringSubmatch(body, 60) {
		for _, g := range m[1:] {
			if g != "" {
				c.Text += " " + html.UnescapeString(g)
			}
		}
	}
	if loc := descDivRe.FindStringIndex(body); loc != nil {
		end := loc[1] + 40000 // long bilingual descriptions: English often comes second
		if end > len(body) {
			end = len(body)
		}
		c.Text += " " + html.UnescapeString(tagRe.ReplaceAllString(body[loc[1]:end], " "))
	}
	// Sites other than itch.io: the page's paragraphs (story, controls...).
	if !IsItchURL(pageURL) {
		c.Text += " " + pageParagraphs(body, 2000)
		if c.CoverURL == "" {
			c.CoverURL = first(twImageRe)
		}
	}
	c.Text = strings.Join(strings.Fields(c.Text), " ")
	// og:title is often "Game by Author".
	if i := strings.LastIndex(c.Title, " by "); i > 0 {
		if c.Author == "" {
			c.Author = strings.TrimSpace(c.Title[i+4:])
		}
		c.Title = strings.TrimSpace(c.Title[:i])
	}
	if p := first(priceMetaRe); p != "" {
		if v, err := strconv.ParseFloat(p, 64); err == nil && v > 0 {
			c.Price = fmt.Sprintf("$%.2f", v)
		}
	} else if d := first(dollarsRe); d != "" && IsPaidPrice(d) {
		c.Price = d
	}
	return c, true
}

// FetchGamePage probes an itch.io URL: ok is false for 404s and non-game
// pages. Used for direct guesses (developer + title slug) and to fill in
// title and price for pages found through web search.
func FetchGamePage(ctx context.Context, client *http.Client, pageURL string) (Candidate, bool, error) {
	body, err := getText(ctx, client, pageURL)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "HTTP 410") {
			return Candidate{}, false, nil
		}
		return Candidate{}, false, err
	}
	c, ok := ParseGamePage(pageURL, body)
	return c, ok, nil
}

// APISearchItch uses itch.io's API search (needs the itch.io API key). The
// response is read loosely because field names vary between API versions.
func APISearchItch(ctx context.Context, client *http.Client, apiKey, query string) ([]Candidate, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no itch.io API key")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.itch.io/search/games?query="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("itch.io API search failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("itch.io API search: HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Games []map[string]any `json:"games"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	var out []Candidate
	for _, g := range raw.Games {
		c := Candidate{URL: str(g, "url"), Title: str(g, "title"),
			CoverURL: str(g, "cover_url", "coverUrl"), Text: str(g, "short_text", "shortText"), Source: "api"}
		if u, ok := g["user"].(map[string]any); ok {
			c.Author = str(u, "display_name", "displayName", "username")
		}
		for _, k := range []string{"min_price", "minPrice"} {
			if v, ok := g[k].(float64); ok && v > 0 {
				c.Price = fmt.Sprintf("$%.2f", v/100) // cents
			}
		}
		if c.URL != "" && c.Title != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

func getText(ctx context.Context, client *http.Client, u string) (string, error) {
	return getTextUA(ctx, client, u, "")
}

func getTextUA(ctx context.Context, client *http.Client, u, ua string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

// SmallCover turns an itch.io "original" image (can be 1500 px and more,
// which the image loader refuses) into the 315x250 thumbnail itch.io serves
// for the same picture.
//
// Not done: the last path part of an itch.io image URL is a signature for
// that exact size, so a rewritten size answers 404. The original is kept and
// the image loader scales big pictures down instead (internal/media).
func SmallCover(u string) string { return u }

// sameAccountLinks lists other game pages of the same itch.io account that a
// page links to.
func sameAccountLinks(pageURL, body string) []string {
	self := NormPageURL(pageURL)
	user := ""
	if i := strings.Index(self, ".itch.io"); i > 0 {
		user = self[:i]
	}
	var out []string
	for _, u := range ExtractItchGameURLs(body) {
		n := NormPageURL(u)
		if n == self || !strings.HasPrefix(n, user+".itch.io/") {
			continue
		}
		out = append(out, u)
		if len(out) == 4 {
			break
		}
	}
	return out
}

// pageParagraphs collects the readable paragraphs of a web page (skipping
// menus, scripts and one-liners), up to max characters.
func pageParagraphs(body string, max int) string {
	body = scriptRe.ReplaceAllString(body, " ")
	var b strings.Builder
	for _, m := range paraRe.FindAllStringSubmatch(body, -1) {
		t := strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(m[1], " "))), " ")
		if len(t) < 40 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(t)
		if b.Len() >= max {
			break
		}
	}
	return b.String()
}
