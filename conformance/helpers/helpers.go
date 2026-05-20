// Package helpers: shared boilerplate for ../scenarios/<n>/main.go.
package helpers

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
)

const (
	KeysDir    = "../../fixtures/keys"
	SourcesDir = "../../fixtures/sources"
	ArchiveDir = "./archive"
	IdsPath    = "./ids.txt"
)

type Scenario struct {
	Title string
	at    time.Time
}

func New(title string, at time.Time) *Scenario {
	fmt.Printf("scenario %s\n\n", title)
	fmt.Printf("outputs:\n  %s\n  %s\n\n", ArchiveDir, IdsPath)
	if err := os.RemoveAll(ArchiveDir); err != nil {
		log.Fatalf("scenario.New: wipe %s: %v", ArchiveDir, err)
	}
	if err := os.RemoveAll(IdsPath); err != nil {
		log.Fatalf("scenario.New: wipe %s: %v", IdsPath, err)
	}
	return &Scenario{Title: title, at: at.UTC()}
}

func (s *Scenario) NextTimestamp(d ...time.Duration) time.Time {
	for _, dd := range d {
		s.at = s.at.Add(dd)
	}
	return s.at
}

func (s *Scenario) ReloadAndVerify(expectBranch, expectHead string) {
	u := Must(ranke.NewFsUniverse(ArchiveDir))
	bth := Must(ranke.NewFsBranchTableHead(filepath.Join(ArchiveDir, "B_h")))
	arc := Must(ranke.NewArchive(u, bth))
	allIds := make(map[string]struct{})
	found := false
	failedBranches := 0
	for _, b := range arc.Branches() {
		head := b.Latest().Head().String()
		fmt.Printf("branch %s → %s\n", b.Name(), head)
		g := Must(arc.GetGraph(b.Latest().Head()))
		count, failed := 0, 0
		err := g.Validate(func(c ranke.Claim, depth int, e error) {
			count++
			indent := strings.Repeat("  ", depth+1)
			mark := "✓"
			if e != nil {
				mark = "✗"
				failed++
			}
			fmt.Printf("%s%s %s  %s\n", indent, mark, c.Node().Type(), shortIdStr(c.ID().String()))
			if e != nil {
				fmt.Printf("%s     %v\n", indent, e)
			}
		})
		fmt.Printf("  %d/%d claims valid\n", count-failed, count)
		if err != nil {
			failedBranches++
		}
		if b.Name() == expectBranch {
			found = true
			if head != expectHead {
				log.Fatalf("scenario.ReloadAndVerify: branch %q head mismatch\n  expected: %s\n  got:      %s",
					expectBranch, expectHead, head)
			}
		}
		for _, id := range CollectIds(g) {
			allIds[id] = struct{}{}
		}
	}
	if failedBranches > 0 {
		log.Fatalf("scenario.ReloadAndVerify: %d branch(es) failed validation", failedBranches)
	}
	if !found {
		log.Fatalf("scenario.ReloadAndVerify: branch %q not found", expectBranch)
	}
	sortedIds := make([]string, 0, len(allIds))
	for id := range allIds {
		sortedIds = append(sortedIds, id)
	}
	sort.Strings(sortedIds)
	Must(os.WriteFile(IdsPath, []byte(strings.Join(sortedIds, "\n")+"\n"), 0o644))
}

// shortIdStr is the same shortener used by the CLI — kept local
// here so this helper has no scenario→cli back-dependency.
func shortIdStr(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

func KeyPath(filename string) string { return filepath.Join(KeysDir, filename) }

func LoadSource(filename string) ([]byte, error) {
	return os.ReadFile(filepath.Join(SourcesDir, filename))
}

// Must panics if any returned value is a non-nil error, else returns
// the first value typed. Works for (T, error), error-only, and
// (T1, T2, ..., error) shapes.
func Must[T any](v T, rest ...any) T {
	if err, isErr := any(v).(error); isErr && err != nil {
		panic(err)
	}
	for _, r := range rest {
		if err, isErr := r.(error); isErr && err != nil {
			panic(err)
		}
	}
	return v
}

func CollectIds(g ranke.Graph) []string {
	seen := map[string]bool{}
	queue := append([]ranke.Id{}, g.Heads()...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		k := id.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		c, ok := g.Get(id)
		if !ok {
			continue
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
