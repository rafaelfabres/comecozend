package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"leaf-hacks/internal/catalog"
	"leaf-hacks/internal/rapatches"
)

// testState is a state over a temporary /roms and data directory, with no
// network: the index and the update check are fresh, so nothing is
// fetched.
func testState(t *testing.T) *state {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	old := romsRoot
	romsRoot = root
	t.Cleanup(func() { romsRoot = old })

	for _, dir := range []string{"gba", "gba/hacks", "nes"} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
	}
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(root, "gba", fmt.Sprintf("Game %02d (USA).gba", i)), []byte(fmt.Sprintf("rom %d", i)), 0o644)
		os.WriteFile(filepath.Join(root, "nes", fmt.Sprintf("Game %02d (USA).nes", i)), append([]byte("NES\x1a"), make([]byte, 16+i)...), 0o644)
	}
	os.WriteFile(filepath.Join(root, "gba", "hacks", "Some Hack.gba"), []byte("hack"), 0o644)

	s := newState(&Config{})
	s.index = rapatches.Index{Fetched: time.Now()}
	s.updates = catalog.UpdateCheck{Checked: time.Now()}
	return s
}

// The SDL thread reads the shared state every frame while background work
// replaces it. This drives that shape — an owner goroutine reading freely
// and applying commits, several pipelines and the periodic check at once,
// and owner-side scans and rebuilds in between — so `go test -race`
// catches any field assigned or read off the owner goroutine.
func TestBackgroundWorkCommitsOnlyThroughTheOwner(t *testing.T) {
	s := testState(t)

	commits := make(chan job)
	s.deliver = func(f func()) {
		ack := make(chan struct{})
		commits <- job{commit: f, ack: ack}
		<-ack
	}

	var workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		workers.Add(2)
		go func() {
			defer workers.Done()
			if err := s.rescan(nil); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer workers.Done()
			_, _, err := s.periodicCheck(context.Background())
			if err != nil && !errors.Is(err, errBusy) {
				t.Error(err)
			}
		}()
	}
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()

	// The owner: what the SDL loop does — apply commits, read state,
	// and now and then change it itself.
	frames := 0
	for {
		select {
		case j := <-commits:
			s.commitHere(j.commit)
			close(j.ack)
		case <-finished:
			if s.lib == nil || len(s.lib.ROMs) != 41 {
				t.Fatalf("library after the rescans: %v", s.lib)
			}
			if _, ok := s.installedPath(catalog.Hack{Title: "Some Hack", ConsoleID: 5}); !ok {
				t.Error("the installed index was not rebuilt with the library")
			}
			return
		default:
			frames++
			if s.lib != nil {
				_ = len(s.lib.ROMs)
			}
			_ = len(s.catalog.Hacks)
			_, _ = s.installedPath(catalog.Hack{Title: "Some Hack", ConsoleID: 5})
			if frames%200 == 0 {
				s.rebuildFromFacts()
				s.commitHere(func() { s.cfg.IncludeTranslations = !s.cfg.IncludeTranslations })
			}
			if frames%2000 == 0 {
				_ = s.scanHere(nil)
			}
		}
	}
}

// While one pipeline runs, the periodic check steps aside instead of
// rebuilding the list underneath it.
func TestPeriodicCheckStepsAsideForARefresh(t *testing.T) {
	s := testState(t)
	s.work.Lock() // a refresh is running
	if _, _, err := s.periodicCheck(context.Background()); !errors.Is(err, errBusy) {
		t.Fatalf("want errBusy, got %v", err)
	}
	s.work.Unlock()
	if _, _, err := s.periodicCheck(context.Background()); err != nil {
		t.Fatalf("with nothing running the check should run: %v", err)
	}
}

func TestIndexChangedSeesReplacedEntries(t *testing.T) {
	a := rapatches.Index{Entries: []rapatches.Entry{{Path: "GBA/Hacks/X/1-a.zip", GameID: 1}, {Path: "GBA/Hacks/Y/2-b.zip", GameID: 2}}}
	same := rapatches.Index{Entries: []rapatches.Entry{a.Entries[1], a.Entries[0]}}
	swapped := rapatches.Index{Entries: []rapatches.Entry{a.Entries[0], {Path: "GBA/Hacks/Z/3-c.zip", GameID: 3}}}
	if indexChanged(a, same) {
		t.Error("the same entries in another order are not a change")
	}
	if !indexChanged(a, swapped) {
		t.Error("one hack removed and another added, same count: that is a change")
	}
}
