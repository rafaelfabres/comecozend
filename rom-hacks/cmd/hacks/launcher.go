package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// esSystem is one <system> entry in EmulationStation's config.
type esSystem struct {
	Name      string       `xml:"name"`
	FullName  string       `xml:"fullname"`
	Path      string       `xml:"path"`
	Command   string       `xml:"command"`
	Extension string       `xml:"extension"` // space-separated, e.g. ".a26 .A26 .bin .zip"
	Emulators []esEmulator `xml:"emulators>emulator"`
}

// esEmulator is one <emulator name="..."> with its <cores>. dArkOS's commands
// reference these through %EMULATOR% and %CORE%.
type esEmulator struct {
	Name  string   `xml:"name,attr"`
	Cores []string `xml:"cores>core"`
	// Command overrides the system-level one for this emulator. dArkOS uses
	// this to send a system to a different program entirely — which is how
	// PICO-8's "retroarch" entry reaches RetroArch + fake08 instead of the
	// pico8.sh wrapper that the system-level command names.
	Command string `xml:"command"`
}

// defaults returns the emulator and core EmulationStation would use when the
// user has not picked alternatives: the first of each, as listed.
func (s esSystem) defaults() (emulator, core string) {
	if len(s.Emulators) == 0 {
		return "", ""
	}
	emulator = s.Emulators[0].Name
	if len(s.Emulators[0].Cores) > 0 {
		core = s.Emulators[0].Cores[0]
	}
	return emulator, core
}

// retroarchDefaultCores names the libretro core to use when a system is sent
// to RetroArch but EmulationStation defines no core for it.
//
// PICO-8 is the case that needs this: its es_systems.cfg entry lists
// "retroarch" as an emulator but declares no cores, because dArkOS routes the
// whole system through a pico8.sh wrapper that picks a program from the mode
// name. Launching RetroArch directly needs the core named explicitly.
var retroarchDefaultCores = map[string]string{
	"pico-8": "fake08",
}

// usesLibretroCore reports whether a command invokes RetroArch with a core,
// as opposed to handing the ROM to a wrapper script.
func usesLibretroCore(command string) bool {
	return strings.Contains(command, "%CORE%") && strings.Contains(command, "-L")
}

// retroarchCommandTemplate borrows a RetroArch invocation from any system
// that has one, so a system whose own command is a wrapper script can still
// be sent straight to RetroArch.
//
// Borrowing rather than hardcoding matters: the binary and core directory
// differ between firmwares and even between RetroArch builds on the same
// device (dArkOS ships both "retroarch" and "retroarch32", each with its own
// cores directory). Copying a template the firmware already uses keeps those
// paths correct.
func retroarchCommandTemplate(list *esSystemList, emulator string) (string, bool) {
	var fallback string
	for _, sys := range list.Systems {
		candidates := []string{sys.Command}
		for _, emu := range sys.Emulators {
			candidates = append(candidates, emu.Command)
		}
		for _, cmd := range candidates {
			if !usesLibretroCore(cmd) {
				continue
			}
			// Prefer a template already written for this exact emulator
			// binary, so retroarch32's core path is not used for retroarch.
			if strings.Contains(cmd, "%EMULATOR%") {
				return cmd, true
			}
			if fallback == "" {
				fallback = cmd
			}
		}
	}
	return fallback, fallback != ""
}

// commandFor returns a named emulator's own <command>, when it has one.
func (s esSystem) commandFor(emulator string) (string, bool) {
	for _, emu := range s.Emulators {
		if strings.EqualFold(emu.Name, emulator) && strings.TrimSpace(emu.Command) != "" {
			return emu.Command, true
		}
	}
	return "", false
}

// coreFor returns the first core listed under a named emulator.
//
// This matters because es_settings.cfg often names an emulator without a
// core: dArkOS records "pico-8.emulator = retroarch" and no "pico-8.core",
// and the core to pair with it (fake08) lives under retroarch's own entry in
// es_systems.cfg. Taking the first emulator's core instead — as this did —
// paired the user's chosen emulator with some other emulator's core.
func (s esSystem) coreFor(emulator string) (string, bool) {
	for _, emu := range s.Emulators {
		if !strings.EqualFold(emu.Name, emulator) {
			continue
		}
		if len(emu.Cores) > 0 {
			return emu.Cores[0], true
		}
		return "", true // the emulator exists but takes no core
	}
	return "", false
}

type esSystemList struct {
	Systems []esSystem `xml:"system"`
}

// esConfigCandidates are the usual locations for es_systems.cfg. The
// per-user copy is checked first because EmulationStation itself prefers it
// (a user override shadows the packaged default).
var esConfigCandidates = []string{
	os.ExpandEnv("$HOME/.emulationstation/es_systems.cfg"),
	"/home/ark/.emulationstation/es_systems.cfg",
	"/etc/emulationstation/es_systems.cfg",
	"/usr/share/emulationstation/es_systems.cfg",
	"/opt/system/es_systems.cfg",
}

// launchCommandFor returns the shell command EmulationStation would run for a
// ROM in the given system folder, with its placeholders filled in.
//
// Reading es_systems.cfg rather than hardcoding a RetroArch invocation means
// the game starts with exactly the core, config and wrapper script dArkOS
// already uses for that system — including whatever per-system tweaks the
// firmware ships.
func launchCommandFor(system, romPath string) (string, error) {
	configPath, list, err := loadESSystems()
	if err != nil {
		return "", err
	}
	for _, sys := range list.Systems {
		if !strings.EqualFold(sys.Name, system) &&
			!strings.EqualFold(filepath.Base(strings.TrimRight(sys.Path, "/")), system) {
			continue
		}
		command := sys.Command
		emulator, core := sys.defaults()
		// EmulationStation stores the user's per-system emulator/core choice
		// separately, in es_settings.cfg. Honouring it is what makes "open
		// with whatever ES is set to" true — otherwise PICO-8 carts always
		// launched the first listed entry (pico8.sh) even when the user had
		// selected the fake08 RetroArch core in the menu.
		if chosenEmu, chosenCore, ok := esUserChoice(system); ok {
			if chosenEmu != "" {
				emulator = chosenEmu
				// Re-pair the core with the chosen emulator: the settings
				// file frequently names only the emulator.
				if matched, found := sys.coreFor(chosenEmu); found {
					core = matched
				}
			}
			if chosenCore != "" {
				core = chosenCore
			}
		}
		// RetroAchievements: RetroArch reports "core not supported" for the
		// Nestopia build shipped with dArkOS, while FCEUmm (RA's reference
		// NES core) works. When the user has not picked a core in
		// EmulationStation, prefer an achievement-capable one if installed.
		if _, _, explicit := esUserChoice(system); !explicit {
			if better, ok := raFriendlyCore(system, core, command); ok {
				fmt.Fprintf(os.Stderr, "system %s: using core %q for RetroAchievements (instead of %q)\n", system, better, core)
				core = better
			}
		}
		// An emulator-specific command wins over the system-level one.
		if specific, ok := sys.commandFor(emulator); ok {
			command = specific
			fmt.Fprintf(os.Stderr, "system %s: using the %s-specific command\n", system, emulator)
		}
		// The user picked RetroArch, but this system's command is a wrapper
		// script that decides for itself what to run. Go to RetroArch
		// directly instead, using a template borrowed from a system that
		// already launches it on this firmware.
		if strings.EqualFold(emulator, "retroarch") || strings.EqualFold(emulator, "retroarch32") {
			if !usesLibretroCore(command) {
				if template, ok := retroarchCommandTemplate(list, emulator); ok {
					if core == "" {
						core = retroarchDefaultCores[strings.ToLower(system)]
					}
					if core != "" {
						command = template
						fmt.Fprintf(os.Stderr, "system %s: bypassing the wrapper, launching RetroArch with core %q\n",
							system, core)
					}
				}
			}
		}

		if strings.TrimSpace(command) == "" {
			return "", fmt.Errorf("system %q in %s has no launch command", system, configPath)
		}
		fmt.Fprintf(os.Stderr, "system %s: launching with emulator=%q core=%q\n", system, emulator, core)
		// A core is not always defined: dArkOS's PICO-8 entry lists
		// emulators ("float-scaled", "pixel-perfect") with no <core> at all,
		// because its launcher script takes the mode rather than a libretro
		// core. Requiring one refused to launch those systems entirely.
		if emulator == "" && strings.Contains(command, "%EMULATOR%") {
			return "", fmt.Errorf("system %q in %s lists no emulator", system, configPath)
		}
		return expandESPlaceholders(command, romPath, emulator, core), nil
	}
	return "", fmt.Errorf("no system %q in %s", system, configPath)
}

// romPathForSystem returns the directory EmulationStation actually scans for
// a system, straight from its <path>.
//
// Hardcoding "/roms/<folder>" was wrong in a way that was invisible until
// tested: dArkOS's PICO-8 entry points at /roms/pico-8/CARTS/, so carts
// downloaded to /roms/pico-8/ were never picked up by the menu. Reading the
// configured path makes this self-correcting for every system.
func romPathForSystem(system string) (string, bool) {
	_, list, err := loadESSystems()
	if err != nil {
		return "", false
	}
	for _, sys := range list.Systems {
		if !strings.EqualFold(sys.Name, system) {
			continue
		}
		path := strings.TrimRight(strings.TrimSpace(sys.Path), "/")
		if path == "" {
			return "", false
		}
		return path, true
	}
	return "", false
}

// esSettingsCandidates are where EmulationStation keeps per-system choices.
var esSettingsCandidates = []string{
	os.ExpandEnv("$HOME/.emulationstation/es_settings.cfg"),
	"/home/ark/.emulationstation/es_settings.cfg",
	"/etc/emulationstation/es_settings.cfg",
}

var esSettingRegex = regexp.MustCompile(`name="([^"]+)"\s+value="([^"]*)"`)

// esUserChoice returns the emulator and core the user selected for a system
// in EmulationStation's own menus, if any.
//
// The file is a flat list of <string name="<system>.emulator" value="..."/>
// entries, so it is read with a regex rather than a schema: ES writes several
// value types into the same file and a strict XML model would reject it.
func esUserChoice(system string) (emulator, core string, ok bool) {
	for _, path := range esSettingsCandidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		wantEmu := strings.ToLower(system) + ".emulator"
		wantCore := strings.ToLower(system) + ".core"
		for _, match := range esSettingRegex.FindAllStringSubmatch(string(data), -1) {
			key := strings.ToLower(match[1])
			switch key {
			case wantEmu:
				emulator, ok = match[2], true
			case wantCore:
				core, ok = match[2], true
			}
		}
		if ok {
			fmt.Fprintf(os.Stderr, "system %s: EmulationStation default emulator=%q core=%q\n",
				system, emulator, core)
			return emulator, core, true
		}
	}
	return "", "", false
}

func loadESSystems() (string, *esSystemList, error) {
	var lastErr error
	for _, path := range esConfigCandidates {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		var list esSystemList
		if err := xml.Unmarshal(data, &list); err != nil {
			lastErr = fmt.Errorf("parse %s: %w", path, err)
			continue
		}
		if len(list.Systems) > 0 {
			return path, &list, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no es_systems.cfg found")
	}
	return "", nil, lastErr
}

// expandESPlaceholders fills the substitutions EmulationStation defines.
// %ROM% is shell-quoted; %ROM_RAW% is deliberately not, matching ES.
func expandESPlaceholders(command, romPath, emulator, core string) string {
	base := filepath.Base(romPath)
	noExt := strings.TrimSuffix(base, filepath.Ext(base))

	replacer := strings.NewReplacer(
		"%ROM%", shellQuote(romPath),
		"%ROM_RAW%", romPath,
		"%BASENAME%", noExt,
		"%GAMEDIR%", filepath.Dir(romPath),
		"%GAMEDIR_RAW%", filepath.Dir(romPath),
		"%EMULATOR%", emulator,
		"%CORE%", core,
	)
	return dropWrapperSegments(replacer.Replace(command))
}

// dropWrapperSegments removes the environment tweaks dArkOS wraps around its
// launch commands, keeping only the emulator invocation itself.
//
// A dArkOS command looks like:
//
//	sudo perfmax %GOVERNOR% %ROM%; nice -n -19 .../retroarch -L ... %ROM%; sudo perfnorm
//
// %GOVERNOR% is filled from EmulationStation's own settings, which live
// outside es_systems.cfg. Rather than guess a value and hand a wrong argument
// to a sudo command, the governor segments are dropped and only the emulator
// invocation is kept. The cost is that the CPU stays on its normal governor
// during play instead of the performance one — the game still runs with the
// right core and configuration.
func dropWrapperSegments(command string) string {
	parts := strings.Split(command, ";")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		// perfmax/perfnorm set the CPU governor; %GOVERNOR% is filled from
		// EmulationStation's own settings, which live outside
		// es_systems.cfg. systemctl segments start/stop helper units such
		// as pico8hotkey — which is not even installed here, so the trailing
		// "stop" failed and made a perfectly good PICO-8 session report
		// "emulator exited with an error: exit status 5". None of these
		// affect whether the game runs.
		if strings.Contains(trimmed, "%GOVERNOR%") ||
			strings.Contains(trimmed, "perfmax") ||
			strings.Contains(trimmed, "perfnorm") ||
			strings.Contains(trimmed, "systemctl") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return command // nothing recognisable; run it as written
	}
	return strings.Join(kept, "; ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// launchGame hands the display to the emulator, runs it to completion, and
// returns. The caller must have suspended the display first: two processes
// cannot hold KMS/DRM at the same time.
func launchGame(system, romPath string) error {
	if _, err := os.Stat(romPath); err != nil {
		return fmt.Errorf("ROM file is missing: %w", err)
	}
	command, err := launchCommandFor(system, romPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "launching:", command)

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		// Include the command, because a non-zero exit here is ambiguous:
		// it can mean the emulator refused to start, or that it ran fine and
		// simply exits non-zero on quit. Seeing the exact invocation is what
		// tells those apart.
		return fmt.Errorf("%w\n  command: %s", err, command)
	}
	return nil
}

// biosFolders are where dArkOS keeps BIOS files (one or two SD cards).
var biosFolders = []string{"/roms/bios", "/roms2/bios"}

func biosHas(names ...string) bool {
	for _, dir := range biosFolders {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return true
			}
		}
	}
	return false
}

// biosHint explains a failed launch when the system is known to need BIOS
// files that are not there. dArkOS's MSX cores (blueMSX, fMSX) do not start
// without them.
func biosHint(system string) string {
	switch strings.ToLower(system) {
	case "msx", "msx2":
		if !biosHas("Machines", "MSX.ROM", "MSX2.ROM") {
			return "MSX needs BIOS files: copy the 'Databases' and 'Machines' folders from " +
				"blueMSXv282full.zip (bluemsx.com) into /roms/bios, or the fMSX BIOS (MSX.ROM, MSX2.ROM, ...)."
		}
		return "The MSX emulator closed with an error. Check the BIOS files in /roms/bios (blueMSX 'Machines' and 'Databases')."
	case "pokemonmini", "pokemini":
		if !biosHas("bios.min") {
			return "The Pokemon Mini emulator closed with an error (optional BIOS: bios.min in /roms/bios)."
		}
	}
	return ""
}

// raCores lists, per system, cores RetroAchievements works with, best first.
var raCores = map[string][]string{
	"nes":     {"fceumm", "mesen"},
	"famicom": {"fceumm", "mesen"},
	"fds":     {"fceumm", "mesen"},
}

// raFriendlyCore returns a better core for achievements when the current
// one is known not to support them and a replacement is installed.
func raFriendlyCore(system, core, command string) (string, bool) {
	prefs, ok := raCores[strings.ToLower(system)]
	if !ok || !strings.Contains(strings.ToLower(core), "nestopia") {
		return "", false
	}
	for _, p := range prefs {
		if coreInstalled(command, p) {
			return p, true
		}
	}
	return "", false
}

// coreInstalled checks the libretro core file the command would load.
func coreInstalled(command, core string) bool {
	for _, f := range strings.Fields(command) {
		if strings.Contains(f, "%CORE%") {
			path := strings.Trim(strings.ReplaceAll(f, "%CORE%", core), `"'`)
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
	}
	for _, dir := range []string{"/home/ark/.config/retroarch/cores", "/home/ark/.config/retroarch32/cores"} {
		if _, err := os.Stat(filepath.Join(dir, core+"_libretro.so")); err == nil {
			return true
		}
	}
	return false
}
