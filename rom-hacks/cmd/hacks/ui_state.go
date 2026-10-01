package main

// baseGame is the active "only hacks of this game" filter. It reuses the
// models' genre row, since a hack has no genre of its own but always has a
// game it patches, and that is the narrowing a user actually wants.
type filterState struct {
	platform string // console short name, "" for all
	sort     string
	show     string
	query    string
	baseGame string
}
