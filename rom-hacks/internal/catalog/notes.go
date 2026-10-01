package catalog

import (
	"regexp"
	"strings"
)

// The readme inside a patch archive is the one place a hack reliably
// describes itself. RetroAchievements has no description field, and the
// comment thread is other players talking about the set — useful, but
// rarely an answer to "what does this hack actually change".
//
// Until now this file was read for one thing, the base ROM's checksum,
// and everything else was discarded. Plenty of them carry a feature list,
// a changelog, credits and instructions right underneath.

var (
	// useWithHeading opens the block that names the base ROM. Everything
	// from there to the end of the checksum lines is machinery, not
	// description.
	useWithHeading = regexp.MustCompile(`(?im)^\s*(use with|apply to|patch to|base rom)\s*:?\s*$`)
	// hashLine is any line that is only a label and a checksum, or a bare
	// checksum: they carry no meaning for a reader.
	hashLine = regexp.MustCompile(`(?i)^\s*((file|bitsize|size|size \(bytes\)|crc32|md5|sha1|sha256|headerless md5)\s*:.*|[0-9a-f]{8}|[0-9a-f]{32}|[0-9a-f]{40}|[0-9a-f]{64}|\(no[- ]intro\)|\(redump\)|no intro|redump)\s*$`)
)

// minNotes is the length below which whatever is left is not worth
// opening a page for.
const minNotes = 40

// ReadmeNotes returns the part of a patch readme that describes the hack,
// with the base-ROM block removed.
func ReadmeNotes(readme string) string {
	if strings.TrimSpace(readme) == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(readme, "\r\n", "\n"), "\n")

	kept := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if useWithHeading.MatchString(line) {
			// Drop the heading and the block under it, until a line
			// appears that is neither blank nor machinery.
			skipping = true
			continue
		}
		if skipping {
			if trimmed == "" || hashLine.MatchString(line) {
				continue
			}
			skipping = false
		}
		if hashLine.MatchString(line) {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}

	text := collapseBlankRuns(kept)
	if len([]rune(strings.TrimSpace(text))) < minNotes {
		return ""
	}
	return text
}

// collapseBlankRuns turns the ragged spacing of a text file into
// paragraphs, so it lays out on a small screen.
func collapseBlankRuns(lines []string) string {
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
