// package: main / cli
// type:    cmd
// job:     read-only CLI to inspect filesystem-backed Ranke-Graph archives
// limits:  no mutation commands; building claims lives in tests + downstream apps (-> tests)
//
// ranke is a small CLI for inspecting filesystem-backed Ranke-Graph
// archives. It opens an archive at <dir> and runs read-only queries
// against it. No mutation commands yet — building claims live in
// the test suite and downstream apps.
//
// Subcommands:
//
//	ranke info <dir>            — high-level summary (B_h, branches)
//	ranke branches <dir>        — list every branch and its head id
//	ranke show <file>           — heuristically decode any archive file (claim or content)
//	ranke show <dir> <id>       — resolve a claim by id via the archive
//	ranke validate <dir>        — verify every branch end-to-end (§5.10)
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flocko-motion/ranke-go"
	histfile "github.com/flocko-motion/ranke-go/adapter/history/file"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	args := os.Args[2:]
	switch os.Args[1] {
	case "info":
		exit(cmdInfo(ctx, args))
	case "branches":
		exit(cmdBranches(ctx, args))
	case "show":
		exit(cmdShow(ctx, args))
	case "validate":
		exit(cmdValidate(ctx, args))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ranke: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ranke — Ranke-Graph archive inspector

usage:
  ranke info     <dir>
  ranke branches <dir>
  ranke show     <file>
  ranke show     <dir> <id>
  ranke validate <dir>

dir is a data bundle: dir/universe/ (claims + content) + dir/branches/B_h. Read-only.`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ranke:", err)
		os.Exit(1)
	}
}

// openArchive opens the bundle at dir: the fs Universe under universe/ plus
// the head-id timeline in branches/B_h, resolved to an Archive at the latest
// head. It returns the Universe too, since closure walks (validate) open a
// graph over it directly.
func openArchive(ctx context.Context, dir string) (ranke.Universe, ranke.Archive, error) {
	u, err := fs.New(filepath.Join(dir, "universe"))
	if err != nil {
		return nil, nil, fmt.Errorf("open universe in %s: %w", dir, err)
	}
	hist, err := histfile.New(filepath.Join(dir, "branches", "B_h"))
	if err != nil {
		return nil, nil, fmt.Errorf("open head timeline in %s: %w", dir, err)
	}
	head, err := hist.Latest(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read latest head in %s: %w", dir, err)
	}
	arc, err := ranke.NewArchive(ctx, u, head.GetId())
	if err != nil {
		return nil, nil, err
	}
	return u, arc, nil
}

// --- info ---

func cmdInfo(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("info: usage: ranke info <dir>")
	}
	_, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("archive %s\n", args[0])
	fmt.Printf("  branches: %d\n", len(branches))
	for _, b := range branches {
		fmt.Printf("    %s → %s\n", b.Name(), shortId(b.Head()))
	}
	return nil
}

// --- branches ---

func cmdBranches(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("branches: usage: ranke branches <dir>")
	}
	_, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	bs, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	if len(bs) == 0 {
		fmt.Println("(no branches)")
		return nil
	}
	for _, b := range bs {
		fmt.Printf("%-20s %s\n", b.Name(), b.Head().String())
	}
	return nil
}

// --- show ---
//
//	ranke show <file>        heuristic: claim if CBOR-decodes, else content
//	ranke show <dir> <id>    open the archive, resolve id with full wiring
func cmdShow(ctx context.Context, args []string) error {
	switch len(args) {
	case 1:
		return showFile(args[0])
	case 2:
		return showInArchive(ctx, args[0], args[1])
	default:
		return fmt.Errorf("show: usage: ranke show <file>  OR  ranke show <dir> <id>")
	}
}

func showFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}
	idStr := filepath.Base(path)
	id, idErr := ranke.ParseId(idStr)
	if idErr == nil {
		if c, err := ranke.DecodeClaim(id, b); err == nil {
			fmt.Printf("file:  %s\n", path)
			fmt.Printf("kind:  claim (%d bytes)\n\n", len(b))
			printClaim(c)
			return nil
		}
	}
	fmt.Printf("file:  %s\n", path)
	fmt.Printf("kind:  content (%d bytes)\n\n", len(b))
	fmt.Println(previewBytes(b))
	return nil
}

func showInArchive(ctx context.Context, dir, idStr string) error {
	_, a, err := openArchive(ctx, dir)
	if err != nil {
		return err
	}
	id, err := ranke.ParseId(idStr)
	if err != nil {
		return fmt.Errorf("show: parse id %q: %w", idStr, err)
	}
	c, err := a.GetClaim(ctx, id)
	if err != nil {
		return fmt.Errorf("show: fetch claim: %w", err)
	}
	printClaim(c)
	return nil
}

// --- validate ---

func cmdValidate(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("validate: usage: ranke validate <dir>")
	}
	u, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	if len(branches) == 0 {
		return fmt.Errorf("validate: archive has no branches")
	}
	totalFailed := 0
	for _, b := range branches {
		fmt.Printf("branch %s → %s\n", b.Name(), b.Head().String())
		g, err := ranke.NewGraphFromClosure(ctx, b.Head(), u)
		if err != nil {
			fmt.Printf("  ✗ open closure: %v\n", err)
			totalFailed++
			continue
		}
		run := g.Verify()
		run.Wait()
		failByID := map[string]error{}
		for _, f := range run.Failures() {
			failByID[f.ID.String()] = f.Err
		}
		count := printClosure(ctx, g, failByID)
		fmt.Printf("  %d/%d claims valid\n", count-len(failByID), count)
		if run.Err() != nil {
			fmt.Printf("  ✗ walk: %v\n", run.Err())
			totalFailed++
		} else if len(failByID) > 0 {
			totalFailed++
		}
	}
	if totalFailed > 0 {
		return fmt.Errorf("validate: %d branch(es) failed", totalFailed)
	}
	return nil
}

// printClosure walks the closure from its open heads breadth-first, printing
// each claim once (✓, or ✗ with its error when it is in failByID) indented by
// depth, and returns the claim count. The per-claim visitor Graph.Validate
// once offered is gone; a closure walk plus Verify's failure set rebuilds the
// same tree.
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
			fmt.Printf("%s✗ %s  %s\n%s     %v\n", indent, c.Node().Type(), shortId(n.id), indent, e)
		} else {
			fmt.Printf("%s✓ %s  %s\n", indent, c.Node().Type(), shortId(n.id))
		}
		for _, e := range c.Edges() {
			queue = append(queue, node{e.Reference(), n.depth + 1})
		}
	}
	return count
}

// --- helpers ---

func shortId(id ranke.Id) string {
	if id == nil {
		return "<nil>"
	}
	s := id.String()
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

func printClaim(c ranke.Claim) {
	n := c.Node()
	fmt.Printf("id:           %s\n", c.ID().String())
	fmt.Printf("type:         %s\n", n.Type())
	fmt.Printf("encoding:     %s\n", n.Encoding())
	fmt.Printf("created_at:   %s\n", n.CreatedAt().Format("2006-01-02T15:04:05.000000000Z"))
	if h := n.GetContentHash(); h != nil {
		fmt.Printf("content_hash: %s\n", h.String())
		fmt.Printf("content_size: %d\n", n.GetContentSize())
		if n.IsContentExternal() {
			fmt.Printf("content:      (external, %d bytes)\n", n.GetContentSize())
		} else if b, err := n.GetInlineContent(); err == nil && b != nil {
			fmt.Printf("content:      %s\n", previewBytes(b))
		}
	}
	if c.Contributor() != nil {
		fmt.Printf("contributor:  %s\n", c.Contributor().ID().String())
	}
	edges := c.Edges()
	if len(edges) > 0 {
		fmt.Printf("edges (%d):\n", len(edges))
		for i, e := range edges {
			fmt.Printf("  [%d] %s\n", i, e.Type())
			fmt.Printf("      reference: %s\n", e.ID().String())
			fmt.Printf("      → %s\n", e.Reference().String())
			if eb, err := e.GetInlineContent(); err == nil && len(eb) > 0 {
				fmt.Printf("      content:   %s\n", previewBytes(eb))
			}
			if e.RelationDirection() != 0 {
				dir := "from"
				if e.RelationDirection() == ranke.RelationTo {
					dir = "to"
				}
				fmt.Printf("      direction: %s\n", dir)
			}
		}
	}
}

func previewBytes(b []byte) string {
	const max = 80
	s := string(b)
	if len(s) > max {
		s = s[:max] + "…"
	}
	// crude printability check
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Sprintf("(binary, %d bytes)", len(b))
		}
	}
	// strip trailing newlines for compactness
	return strings.TrimRight(s, "\n\r")
}
