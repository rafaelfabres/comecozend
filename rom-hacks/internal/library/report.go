package library

import (
	"path/filepath"
	"sort"
	"strings"
)

// Report explains what a scan ignored.
//
// A folder that comes back with zero ROMs is the most confusing thing this
// app can show, because the user can see the games sitting right there.
// Almost always the answer is dull — a disc system, an extension the
// console table does not list, a folder name this app does not recognise —
// and saying which turns a mystery into a one-line fix.
type Report struct {
	// SkippedFolders maps a folder under /roms to why it was not scanned.
	// Only folders that actually held something ROM-shaped appear here.
	SkippedFolders map[string]string
	// UnknownExts maps a folder to the extensions found there that no
	// console in the table claims, with a count each.
	UnknownExts map[string]map[string]int

	// Oversized maps a folder to files skipped for being larger than any
	// cartridge dump.
	Oversized map[string][]string

	skipped    map[string]string
	hasContent map[string]bool
}

func newReport() *Report {
	return &Report{
		SkippedFolders: map[string]string{},
		UnknownExts:    map[string]map[string]int{},
		Oversized:      map[string][]string{},
		skipped:        map[string]string{},
		hasContent:     map[string]bool{},
	}
}

// finish keeps only the skipped folders worth mentioning: the ones that
// held files. /roms on a full device has eighty folders for systems this
// app has no business scanning, and listing them all taught the reader to
// skip the whole report.
func (r *Report) finish() {
	if r == nil {
		return
	}
	for folder, why := range r.skipped {
		if r.hasContent[folder] {
			r.SkippedFolders[folder] = why
		}
	}
}

// metadataExts are the files every ROM folder carries: EmulationStation's
// gamelist and its backup, notes, box art. They are not ROMs anybody
// expected to be scanned, so reporting them is noise.
var metadataExts = map[string]bool{
	".xml": true, ".old": true, ".txt": true, ".png": true, ".jpg": true,
	".jpeg": true, ".cfg": true, ".srm": true, ".state": true, ".sav": true,
	".db": true, ".json": true, ".md": true, ".bak": true, ".ips": true,
}

func (r *Report) skipFolder(folder, why string) {
	if r == nil {
		return
	}
	r.skipped[folder] = why
}

func (r *Report) note(folder, filename string) {
	if r == nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = "(no extension)"
	}
	if metadataExts[ext] {
		return
	}
	byExt := r.UnknownExts[folder]
	if byExt == nil {
		byExt = map[string]int{}
		r.UnknownExts[folder] = byExt
	}
	byExt[ext]++
}

// tooBig records a file skipped for its size, so a missing game is never
// a silent omission.
func (r *Report) tooBig(folder, filename string) {
	if r == nil {
		return
	}
	r.Oversized[folder] = append(r.Oversized[folder], filename)
	r.sawCandidate(folder)
}

// sawCandidate records that a folder holds at least one file that could
// plausibly be a ROM. A folder of themes, saves or BIOS files never does,
// and a report that names all eighty of them buries the one line that
// matters.
func (r *Report) sawCandidate(folder string) {
	if r != nil {
		r.hasContent[folder] = true
	}
}

// Lines renders the report as something printable, most useful first.
func (r *Report) Lines() []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, folder := range sortedKeys(r.SkippedFolders) {
		out = append(out, "skipped "+folder+": "+r.SkippedFolders[folder])
	}
	for folder, files := range r.Oversized {
		for _, f := range files {
			out = append(out, folder+": too large to hash - "+f)
		}
	}
	for _, folder := range sortedExtFolders(r.UnknownExts) {
		byExt := r.UnknownExts[folder]
		var parts []string
		for _, ext := range sortedByCount(byExt) {
			parts = append(parts, ext)
		}
		out = append(out, folder+": ignored files ending in "+strings.Join(parts, ", "))
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedExtFolders(m map[string]map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedByCount(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return out[i] < out[j]
	})
	for i, k := range out {
		out[i] = k + " (" + itoa(m[k]) + ")"
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
