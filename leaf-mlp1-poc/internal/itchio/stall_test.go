package itchio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A connection that stops sending must end the download, not hang it:
// the client has no overall timeout (big files take minutes), so before
// the watchdog a Wi-Fi drop left "Downloading..." on screen for good.
func TestStreamToFileGivesUpOnAStalledConnection(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first bytes"))
		w.(http.Flusher).Flush()
		select { // then nothing, as if the network went away
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	old := StallTimeout
	StallTimeout = 200 * time.Millisecond
	defer func() { StallTimeout = old }()

	c := &Client{http: srv.Client()}
	dest := filepath.Join(t.TempDir(), "game.bin")
	done := make(chan error, 1)
	go func() { done <- c.streamToFileContext(context.Background(), srv.URL, dest, nil) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDownloadStalled) {
			t.Fatalf("want ErrDownloadStalled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download hung on a stalled connection")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a partial file was committed")
	}
}

// A slow but moving download is not a stall: each byte resets the clock.
func TestStreamToFileToleratesSlowProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 5; i++ {
			w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	old := StallTimeout
	StallTimeout = 300 * time.Millisecond
	defer func() { StallTimeout = old }()

	c := &Client{http: srv.Client()}
	dest := filepath.Join(t.TempDir(), "game.bin")
	if err := c.streamToFileContext(context.Background(), srv.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "xxxxx" {
		t.Errorf("got %q", b)
	}
}
