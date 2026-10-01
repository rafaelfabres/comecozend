// Command seed-index writes the RAPatches listing that ships with the app.
//
// Shipping a first-run index matters for two reasons: the app has
// something to show before its first network call, and GitHub's
// unauthenticated API allows only 60 requests an hour per address, which
// is generous for one device but not for a room full of them.
//
// Usage:
//
//	seed-index data/rapatches-index.json            fetch from GitHub
//	seed-index data/rapatches-index.json paths.txt  build from a tree listing
//
// The second form takes the output of:
//
//	git clone --depth 1 --filter=blob:none --no-checkout \
//	    https://github.com/RetroAchievements/RAPatches.git
//	cd RAPatches && git ls-tree -r --name-only HEAD > paths.txt
//
// which downloads the tree without any patch payload — useful when the
// API is rate limited.
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"leaf-hacks/internal/rapatches"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: seed-index <out.json> [paths.txt]")
		os.Exit(2)
	}
	out := os.Args[1]

	var ix rapatches.Index
	var err error
	if len(os.Args) > 2 {
		ix, err = fromFile(os.Args[2])
	} else {
		ix, err = rapatches.Fetch(context.Background(), &http.Client{Timeout: 90 * time.Second})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := ix.Save(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	byCategory := map[rapatches.Category]int{}
	byConsole := map[string]int{}
	for _, e := range ix.Entries {
		byCategory[e.Category]++
		if e.Category == rapatches.Hacks {
			byConsole[e.Console]++
		}
	}
	fmt.Printf("%s: %d entries\n", out, len(ix.Entries))
	for _, c := range sortedKeys(byCategory) {
		fmt.Printf("  %-12s %d\n", c, byCategory[rapatches.Category(c)])
	}
	fmt.Println("hacks by console:")
	for _, c := range sortedByCount(byConsole) {
		fmt.Printf("  %-14s %d\n", c, byConsole[c])
	}
}

func fromFile(path string) (rapatches.Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return rapatches.Index{}, err
	}
	defer f.Close()
	var paths []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		paths = append(paths, sc.Text())
	}
	return rapatches.IndexFromPaths(paths, time.Now()), sc.Err()
}

func sortedKeys(m map[rapatches.Category]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
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
	return out
}
