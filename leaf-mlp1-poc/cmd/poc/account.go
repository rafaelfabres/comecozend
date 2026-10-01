package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"leaf-mlp1-poc/internal/appui"
	"leaf-mlp1-poc/internal/itchio"
)

// account holds what we know about the signed-in itch.io user. It is filled
// in by a background validation of the stored API key.
type account struct {
	username string
	owned    []itchio.OwnedGame
	checked  bool
	err      error
}

func (a account) signedIn() bool { return a.username != "" }

// label is the short status shown in the list header.
func (a account) label() string {
	switch {
	case a.username != "":
		return fmt.Sprintf("%s  (%d owned)", a.username, len(a.owned))
	case a.err != nil:
		return "sign-in failed"
	case a.checked:
		return "not signed in"
	default:
		return ""
	}
}

// verifyAndReport validates a key from the terminal and prints the result.
func verifyAndReport(apiKey string) {
	client := itchio.NewClient()
	username, owned, err := client.ValidateAPIKey(apiKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key rejected:", err)
		return
	}
	fmt.Printf("signed in as %s — %d owned game(s)\n", username, len(owned))
}

// buildOwnedModel lists the user's purchases in the vendored ManageModel's
// shape, reusing the same popup the downloads window uses.
func buildOwnedModel(acct account, visibleRows int) *appui.ManageModel {
	model := appui.NewManageModel("Your itch.io purchases")
	model.VisibleRows = visibleRows

	owned := append([]itchio.OwnedGame(nil), acct.owned...)
	sort.SliceStable(owned, func(i, j int) bool {
		return strings.ToLower(owned[i].Title) < strings.ToLower(owned[j].Title)
	})

	items := make([]appui.ManageItem, 0, len(owned))
	for i, game := range owned {
		items = append(items, appui.ManageItem{
			Kind:      appui.ManageItemFile,
			Label:     game.Title,
			Detail:    game.URL,
			FileIndex: i,
			Enabled:   true,
		})
	}

	subtitle := fmt.Sprintf("Signed in as %s  -  %d game(s)", acct.username, len(owned))
	switch {
	case !acct.signedIn() && acct.err != nil:
		subtitle = "Sign-in failed: " + acct.err.Error()
	case !acct.signedIn():
		subtitle = "Not signed in. START > Settings to add an itch.io API key."
	case len(owned) == 0:
		subtitle = fmt.Sprintf("Signed in as %s - no purchases found", acct.username)
	}
	model.SetItems(subtitle, items)
	return model
}
