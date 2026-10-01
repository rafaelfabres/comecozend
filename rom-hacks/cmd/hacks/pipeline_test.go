package main

import (
	"testing"
	"time"

	"leaf-hacks/internal/catalog"
	"leaf-hacks/internal/library"
)

// The installed marker must mean "this hack is on the card", not "I own
// the game it patches". Owning the base game is why a hack appears in the
// list at all, so matching against the whole library marked nearly every
// row as installed — and the game page then offered Play for a hack that
// had never been installed.
func TestInstalledOnlyCountsTheHacksFolder(t *testing.T) {
	st := &state{cfg: &Config{}, lib: &library.Library{ROMs: []library.ROM{
		{Path: "/roms/nes/Ninja Gaiden (USA).zip", ConsoleID: 7, ModTime: time.Now()},
		{Path: "/roms/nes/hacks/Ninja Gaiden - The Dragon Scroll.nes", ConsoleID: 7, ModTime: time.Now()},
	}}}
	st.buildInstalledIndex()

	base := catalog.Hack{GameID: 1859, Title: "Ninja Gaiden", ConsoleID: 7}
	if path, ok := st.installedPath(base); ok {
		t.Errorf("the base game was reported as an installed hack: %s", path)
	}

	hack := catalog.Hack{GameID: 13422, Title: "Ninja Gaiden - The Dragon Scroll", ConsoleID: 7}
	path, ok := st.installedPath(hack)
	if !ok {
		t.Fatal("the hack in the hacks folder was not found")
	}
	if path != "/roms/nes/hacks/Ninja Gaiden - The Dragon Scroll.nes" {
		t.Errorf("found %s", path)
	}
}

// A hack on one system must not be matched by a same-named file on
// another.
func TestInstalledIsPerConsole(t *testing.T) {
	st := &state{cfg: &Config{}, lib: &library.Library{ROMs: []library.ROM{
		{Path: "/roms/gba/hacks/Some Hack.gba", ConsoleID: 5},
	}}}
	st.buildInstalledIndex()

	if _, ok := st.installedPath(catalog.Hack{Title: "Some Hack", ConsoleID: 7}); ok {
		t.Error("a GBA hack was matched for an NES entry")
	}
	if _, ok := st.installedPath(catalog.Hack{Title: "Some Hack", ConsoleID: 5}); !ok {
		t.Error("the GBA hack was not matched on its own system")
	}
}

// Only one install at a time. The guard used to be the shared busy flag,
// which every finished background job cleared — so opening a second hack
// while the first was patching re-enabled the button, and two patches ran
// at once over the same folder.
func TestOnlyOneInstallRunsAtATime(t *testing.T) {
	var run installRun

	if run.busy() {
		t.Fatal("nothing should be running yet")
	}
	run.start(100, "First hack")
	if !run.busy() {
		t.Fatal("an install is running")
	}

	// The page for a different hack must see the lock.
	if _, mine := run.state(200); mine {
		t.Error("another hack's page should not claim the running install")
	}
	if status, mine := run.state(100); !mine || status == "" {
		t.Errorf("its own page should show the status, got %q %v", status, mine)
	}

	run.set("Patching 40%")
	if status, _ := run.state(100); status != "Patching 40%" {
		t.Errorf("status = %q", status)
	}

	run.finish()
	if run.busy() {
		t.Error("the lock should be released when the install ends")
	}
}
