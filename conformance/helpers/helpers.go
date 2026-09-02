// package: helpers / conformance
// type:    test
// job:     shared boilerplate for ../scenarios/<n>/main.go (bundle setup, reload+verify, id collection)
// limits:  builds no claims itself; scenarios do that inline (-> conformance/scenarios)
//
// Package helpers: shared boilerplate for ../scenarios/<n>/main.go.
package helpers

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/fs"
)

const (
	KeysDir    = "../../fixtures/keys"
	SourcesDir = "../../fixtures/sources"
	// DataDir is the scenario's output bundle: tar it and you have
	// everything a verifier needs.
	DataDir     = "./data"
	UniverseDir = DataDir + "/universe"
	// BookmarkIdPath holds the one bookmark id the bundle is reopened from.
	BookmarkIdPath = DataDir + "/branches/B_h"
	IdsPath        = DataDir + "/ids.txt"
)

// Scenario holds the running state of one conformance scenario: its
// title and an advancing logical clock used to stamp claims.
type Scenario struct {
	Title string
	at    time.Time
}

// New wipes the scenario's output bundle and returns a fresh Scenario
// whose logical clock starts at at.
func New(title string, at time.Time) *Scenario {
	fmt.Printf("scenario %s\n\n", title)
	fmt.Printf("output bundle:\n  %s/\n    universe/   (claims, content, bookmarks)\n    branches/B_h\n    ids.txt\n\n", DataDir)
	if err := os.RemoveAll(DataDir); err != nil {
		log.Fatalf("scenario.New: wipe %s: %v", DataDir, err)
	}
	return &Scenario{Title: title, at: at.UTC()}
}

// NextTimestamp advances the scenario clock by the given durations (if
// any) and returns the new current time.
func (s *Scenario) NextTimestamp(d ...time.Duration) time.Time {
	for _, dd := range d {
		s.at = s.at.Add(dd)
	}
	return s.at
}

// Tick advances the clock by one second, satisfying the dev Sequencer's Clock
// so scenario claims and minted branch tables share one monotone timeline.
func (s *Scenario) Tick() time.Time { return s.NextTimestamp(time.Second) }

// WriteBookmarkId persists one of seq's bookmark ids to BookmarkIdPath — the one value
// (foundation paper §Backup) a bundle must carry to be reopened, since there is no way
// to discover it. A scenario calls this once its Sequencer has bootstrapped, before
// ReloadAndVerify (or any other process) needs it.
func WriteBookmarkId(seq ranke.Sequencer) {
	if err := os.MkdirAll(filepath.Dir(BookmarkIdPath), 0o755); err != nil {
		log.Fatalf("scenario.WriteBookmarkId: mkdir: %v", err)
	}
	Must(0, os.WriteFile(BookmarkIdPath, []byte(seq.BookmarkId().String()+"\n"), 0o644))
}

// ReloadAndVerify reopens the persisted bundle at its latest head, validates every
// branch closure, requires expectBranch among them, and writes ids.txt. What the head
// ids are is the reference bundle's business, so nothing here pins one: a value in
// code could only be corrected by hand on every intentional change.
func (s *Scenario) ReloadAndVerify(ctx context.Context, expectBranch string) {
	u := Must(fs.New(UniverseDir))
	id := Must(ranke.ParseId(strings.TrimSpace(string(Must(os.ReadFile(BookmarkIdPath))))))
	marks := Must(ranke.OpenBookmarks(ctx, u.Bookmarks(), u, id))
	head := Must(marks.Latest(ctx)).Head() // the archive head k the list advanced to
	arc := Must(ranke.NewArchive(ctx, u, head))

	allIds := make(map[string]struct{})
	found := false
	failedBranches := 0
	for _, b := range Must(arc.GetBranches(ctx)) {
		branchHead := b.Head()
		fmt.Printf("branch %s → %s\n", b.Name(), branchHead.String())

		g := Must(ranke.NewGraphFromClosure(ctx, branchHead, u))
		run := g.Verify()
		run.Wait()
		failByID := map[string]error{}
		for _, f := range run.Failures() {
			failByID[f.ID.String()] = f.Err
		}
		count := printClosure(ctx, g, failByID)
		fmt.Printf("  %d/%d claims valid\n", count-len(failByID), count)
		if run.Err() != nil {
			fmt.Printf("  walk error: %v\n", run.Err())
			failedBranches++
		} else if len(failByID) > 0 {
			failedBranches++
		}

		if b.Name() == expectBranch {
			found = true
		}
		for _, id := range CollectIds(ctx, g) {
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

// printClosure walks the closure breadth-first from the open heads, printing each
// claim once (✓, or ✗ plus its failByID error) at its depth, and counts them.
func printClosure(ctx context.Context, g ranke.Graph, failByID map[string]error) int {
	type node struct {
		id    ranke.Id
		depth int
	}
	seen := map[string]bool{}
	queue := make([]node, 0)
	for _, h := range g.Heads() {
		queue = append(queue, node{h, 0})
	}
	count := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		key := n.id.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		c, err := g.GetClaim(ctx, n.id)
		if err != nil {
			continue
		}
		count++
		indent := strings.Repeat("  ", n.depth+1)
		if e, bad := failByID[key]; bad {
			fmt.Printf("%s✗ %s  %s\n%s     %v\n", indent, c.Node().Type(), shortIdStr(key), indent, e)
		} else {
			fmt.Printf("%s✓ %s  %s\n", indent, c.Node().Type(), shortIdStr(key))
		}
		for _, e := range c.Edges() {
			queue = append(queue, node{e.Reference(), n.depth + 1})
		}
	}
	return count
}

// shortIdStr truncates an id to 20 characters, as the CLI does.
func shortIdStr(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

// KeyPath returns the path to filename inside the fixtures keys dir.
func KeyPath(filename string) string { return filepath.Join(KeysDir, filename) }

// LoadSource reads filename from the fixtures sources dir.
func LoadSource(filename string) ([]byte, error) {
	return os.ReadFile(filepath.Join(SourcesDir, filename))
}

// Must panics on any non-nil error among the values, else returns the first
// one typed — for (T, error), error-only, and (T1, ..., error) shapes.
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

// CollectIds walks the graph from its heads and returns the sorted set
// of all reachable claim ids.
func CollectIds(ctx context.Context, g ranke.Graph) []string {
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
		c, err := g.GetClaim(ctx, id)
		if err != nil {
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
