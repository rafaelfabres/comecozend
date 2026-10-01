package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withES points the launcher at a temporary es_systems.cfg and
// es_settings.cfg.
func withES(t *testing.T, systems, settings string) {
	t.Helper()
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "es_systems.cfg")
	setPath := filepath.Join(dir, "es_settings.cfg")
	os.WriteFile(sysPath, []byte(systems), 0o644)
	os.WriteFile(setPath, []byte(settings), 0o644)
	oldSys, oldSet := esConfigCandidates, esSettingsCandidates
	esConfigCandidates, esSettingsCandidates = []string{sysPath}, []string{setPath}
	t.Cleanup(func() { esConfigCandidates, esSettingsCandidates = oldSys, oldSet })
}

// Choosing only the emulator in EmulationStation is not choosing a core,
// so the swap from Nestopia (no achievements) to FCEUmm still applies.
func TestNESCoreSwapAppliesWhenOnlyTheEmulatorWasChosen(t *testing.T) {
	cores := t.TempDir()
	os.WriteFile(filepath.Join(cores, "fceumm_libretro.so"), nil, 0o644)
	withES(t, `<systemList><system>
  <name>nes</name><path>/roms/nes</path>
  <command>/usr/local/bin/%EMULATOR% -L `+cores+`/%CORE%_libretro.so %ROM%</command>
  <emulators><emulator name="retroarch"><cores><core>nestopia</core><core>fceumm</core></cores></emulator></emulators>
</system></systemList>`,
		`<string name="nes.emulator" value="retroarch" />`)

	cmd, err := launchCommandFor("nes", "/roms/nes/Game.nes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "fceumm_libretro.so") {
		t.Errorf("still launching Nestopia: %s", cmd)
	}
}

// A core the user picked is respected, even Nestopia.
func TestNESCoreTheUserPickedIsKept(t *testing.T) {
	cores := t.TempDir()
	os.WriteFile(filepath.Join(cores, "fceumm_libretro.so"), nil, 0o644)
	withES(t, `<systemList><system>
  <name>nes</name><path>/roms/nes</path>
  <command>/usr/local/bin/%EMULATOR% -L `+cores+`/%CORE%_libretro.so %ROM%</command>
  <emulators><emulator name="retroarch"><cores><core>fceumm</core><core>nestopia</core></cores></emulator></emulators>
</system></systemList>`,
		`<string name="nes.emulator" value="retroarch" /><string name="nes.core" value="nestopia" />`)

	cmd, err := launchCommandFor("nes", "/roms/nes/Game.nes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "nestopia_libretro.so") {
		t.Errorf("the user's core was replaced: %s", cmd)
	}
}

// Sending a wrapper-script system straight to RetroArch borrows another
// system's command. It must be one for the binary asked for: retroarch32's
// template carries retroarch32's cores folder.
func TestRetroArchTemplateMatchesTheBinary(t *testing.T) {
	list := &esSystemList{Systems: []esSystem{
		{Name: "gb", Command: "/usr/local/bin/retroarch32 -L /home/ark/.config/retroarch32/cores/%CORE%_libretro.so %ROM%"},
		{Name: "gba", Command: "/usr/local/bin/retroarch -L /home/ark/.config/retroarch/cores/%CORE%_libretro.so %ROM%"},
	}}
	got, ok := retroarchCommandTemplate(list, "retroarch")
	if !ok || !strings.Contains(got, "/retroarch -L") {
		t.Errorf("for retroarch got %q", got)
	}
	got, _ = retroarchCommandTemplate(list, "retroarch32")
	if !strings.Contains(got, "/retroarch32 -L") {
		t.Errorf("for retroarch32 got %q", got)
	}
	// A template that takes the binary from %EMULATOR% fits either.
	list.Systems = append(list.Systems, esSystem{Name: "nes", Command: "/usr/local/bin/%EMULATOR% -L /home/ark/.config/%EMULATOR%/cores/%CORE%_libretro.so %ROM%"})
	if got, _ := retroarchCommandTemplate(list, "retroarch"); !strings.Contains(got, "%EMULATOR%") {
		t.Errorf("the generic template should win, got %q", got)
	}
}
