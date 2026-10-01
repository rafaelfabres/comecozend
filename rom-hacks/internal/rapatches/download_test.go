package rapatches

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// An archive past the cap is refused, not cut to the cap and handed on:
// the truncated file then failed as "not a valid zip", which names the
// wrong problem.
func TestDownloadToRefusesInsteadOfTruncating(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Streamed without a Content-Length, so only the copy can notice.
		w.(http.Flusher).Flush()
		w.Write([]byte(body))
	}))
	defer srv.Close()
	defer SetRawBaseForTest(srv.URL + "/")()
	old := maxArchive
	maxArchive = 10
	defer func() { maxArchive = old }()

	dir := t.TempDir()
	e := Entry{Path: "GBA/Hacks/Game/1-hack.zip", File: "1-hack.zip"}

	body = strings.Repeat("x", 11)
	if _, err := DownloadTo(context.Background(), srv.Client(), e, dir); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a partial download was left behind: %v", entries)
	}

	body = strings.Repeat("x", 10) // exactly at the cap is fine
	path, err := DownloadTo(context.Background(), srv.Client(), e, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); len(b) != 10 {
		t.Errorf("got %d bytes, want 10", len(b))
	}
}

func TestLoadReportsTheReadError(t *testing.T) {
	broken := errors.New("zip: checksum error")
	p := PatchFile{Name: "hack.bps", load: func() ([]byte, error) { return nil, broken }}
	if _, err := p.Load(); !errors.Is(err, broken) {
		t.Errorf("Load hid the error: %v", err)
	}
	if p.Bytes() != nil {
		t.Error("Bytes should still return nil on a read error")
	}
}
