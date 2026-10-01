package rahub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebPageUploads(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blinkysrevenge-gbc/":
			w.Write([]byte(`<a href="https://www.wls.hu/gbc/blinkysrevenge/index.html">play online</a>
			<a class="btn" href="/gbc/blinkysrevenge/blinkysrevenge.zip"><span>download</span></a>
			<a href="/wp-content/uploads/box.png">box</a>`))
		case "/gbc/blinkysrevenge/blinkysrevenge.zip":
			w.Write([]byte("PK\x03\x04zipdata"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ups, err := WebPageUploads(context.Background(), srv.Client(), srv.URL+"/blinkysrevenge-gbc/")
	if err != nil || len(ups) != 1 || ups[0].Name != "blinkysrevenge.zip" {
		t.Fatalf("uploads: %+v %v", ups, err)
	}
	dest := filepath.Join(t.TempDir(), "dl")
	if err := DownloadPlain(context.Background(), srv.Client(), ups[0].URL, dest, 1<<20); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "PK\x03\x04zipdata" {
		t.Error("content")
	}
	if err := DownloadPlain(context.Background(), srv.Client(), ups[0].URL, dest, 4); err == nil {
		t.Error("size cap not applied")
	}
	if IsItchURL("https://wls.hu/x") || !IsItchURL("https://gcup.itch.io/casanova") {
		t.Error("IsItchURL")
	}
}

func TestOtherSitePageInfo(t *testing.T) {
	page := `<html><head><meta property="og:title" content="Blinky's Revenge - An original release from WLS">
	<meta property="og:image" content="https://wls.hu/wp-content/uploads/2026/03/Blinkys-Revenge-GBC-WLSBOX-01-Egyeni.png">
	<meta property="og:description" content="An original release from WLS to Nintendo's Game Boy handheld console."></head>
	<body><nav><p>Menu menu menu menu menu menu menu menu menu menu</p></nav>
	<p>Have you had enough of the pac-man always winning? Have you ever wanted to side with the evil one?</p>
	<p>ok</p><script>var x = "<p>not this one, it is inside a script tag</p>";</script>
	<p>With Blinky – this little red ghost – you have to catch Pac-Man in this 12-level game.</p></body></html>`
	c, ok := ParseGamePage("https://wls.hu/blinkysrevenge-gbc/", page)
	if !ok || !strings.Contains(c.CoverURL, "Blinkys-Revenge-GBC") {
		t.Fatalf("cover: %+v", c)
	}
	if !strings.Contains(c.Text, "little red ghost") || strings.Contains(c.Text, "Menu menu") || strings.Contains(c.Text, "script tag") {
		t.Errorf("text: %q", c.Text)
	}
}
