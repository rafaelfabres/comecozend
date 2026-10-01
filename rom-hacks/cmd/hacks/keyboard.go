package main

import (
	"strings"

	"leaf-hacks/internal/appui"
)

// keyboardRows is a simple QWERTY-ish grid plus control keys. There is no
// vendored on-screen keyboard to reuse — Catastrophe's own text entry is
// native and closed-source — so this is purpose-built for the PoC.
var keyboardRows = [][]string{
	{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"},
	{"Q", "W", "E", "R", "T", "Y", "U", "I", "O", "P"},
	{"A", "S", "D", "F", "G", "H", "J", "K", "L"},
	{"Z", "X", "C", "V", "B", "N", "M"},
	{"SPACE", "BACKSPACE", "CLEAR", "DONE"},
}

type keyboardModel struct {
	row, col int
	query    []rune
}

func newKeyboardModel(initial string) *keyboardModel {
	return &keyboardModel{query: []rune(initial)}
}

func (k *keyboardModel) String() string { return string(k.query) }

// Handle processes one input event. done=true means the caller should
// commit k.String() as the new query; cancelled=true means discard it.
func (k *keyboardModel) Handle(event appui.InputEvent) (done, cancelled bool) {
	if !event.Pressed {
		return false, false
	}
	switch event.Button {
	case appui.ButtonUp:
		if k.row > 0 {
			k.row--
			k.clampCol()
		}
	case appui.ButtonDown:
		if k.row < len(keyboardRows)-1 {
			k.row++
			k.clampCol()
		}
	case appui.ButtonLeft:
		if k.col > 0 {
			k.col--
		}
	case appui.ButtonRight:
		if k.col < len(keyboardRows[k.row])-1 {
			k.col++
		}
	case appui.ButtonA:
		if k.press() {
			return true, false
		}
	case appui.ButtonY:
		k.backspace()
	case appui.ButtonB, appui.ButtonQuit:
		return false, true
	case appui.ButtonStart:
		return true, false
	}
	return false, false
}

func (k *keyboardModel) clampCol() {
	if max := len(keyboardRows[k.row]) - 1; k.col > max {
		k.col = max
	}
}

func (k *keyboardModel) backspace() {
	if len(k.query) > 0 {
		k.query = k.query[:len(k.query)-1]
	}
}

// press applies the currently-selected key and reports whether it was DONE.
func (k *keyboardModel) press() bool {
	switch key := keyboardRows[k.row][k.col]; key {
	case "SPACE":
		k.query = append(k.query, ' ')
	case "BACKSPACE":
		k.backspace()
	case "CLEAR":
		k.query = nil
	case "DONE":
		return true
	default:
		k.query = append(k.query, []rune(strings.ToLower(key))...)
	}
	return false
}
