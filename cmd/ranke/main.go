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

func openArchive(ctx context.Context, dir string) (ranke.Archive, error) {
	u, err := ranke.NewFsUniverse(filepath.Join(dir, "universe"))
	if err != nil {
		return nil, fmt.Errorf("open universe in %s: %w", dir, err)
	}
	bth, err := ranke.NewFsBranchTableHead(filepath.Join(dir, "branches", "B_h"))
	if err != nil {
		return nil, fmt.Errorf("open branch table head in %s: %w", dir, err)
	}
	return ranke.NewArchive(ctx, u, bth)
}

// --- info ---

func cmdInfo(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("info: usage: ranke info <dir>")
	}
	a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches := a.Branches(ctx)
	fmt.Printf("archive %s\n", args[0])
	fmt.Printf("  branches: %d\n", len(branches))
	for _, b := range branches {
		fmt.Printf("    %s → %s\n", b.Name(), shortId(b.Latest().Head()))
	}
	return nil
}

// --- branches ---

func cmdBranches(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("branches: usage: ranke branches <dir>")
	}
	a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	bs := a.Branches(ctx)
	if len(bs) == 0 {
		fmt.Println("(no branches)")
		return nil
	}
	for _, b := range bs {
		fmt.Printf("%-20s %s\n", b.Name(), b.Latest().Head().String())
		if prov := b.Provenance(); len(prov) > 0 {
			for _, e := range prov {
				fmt.Printf("  ← %s  (%s)\n", shortId(e.Head()), e.Time().Format("2006-01-02T15:04:05Z"))
			}
		}
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
	a, err := openArchive(ctx, dir)
	if err != nil {
		return err
	}
	id, err := ranke.ParseId(idStr)
	if err != nil {
		return fmt.Errorf("show: parse id %q: %w", idStr, err)
	}
	g, err := a.GetGraph(ctx, id)
	if err != nil {
		return fmt.Errorf("show: fetch claim: %w", err)
	}
	c, ok := g.Get(id)
	if !ok {
		return fmt.Errorf("show: claim %s not in graph", id.String())
	}
	printClaim(c)
	return nil
}

// --- validate ---

func cmdValidate(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("validate: usage: ranke validate <dir>")
	}
	a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches := a.Branches(ctx)
	if len(branches) == 0 {
		return fmt.Errorf("validate: archive has no branches")
	}
	totalFailed := 0
	for _, b := range branches {
		fmt.Printf("branch %s → %s\n", b.Name(), b.Latest().Head().String())
		g, err := a.GetGraph(ctx, b.Latest().Head())
		if err != nil {
			fmt.Printf("  ✗ load graph: %v\n", err)
			totalFailed++
			continue
		}
		count, failed := 0, 0
		err = g.Validate(func(c ranke.Claim, depth int, e error) {
			count++
			indent := strings.Repeat("  ", depth+1)
			mark := "✓"
			if e != nil {
				mark = "✗"
				failed++
			}
			fmt.Printf("%s%s %s  %s\n", indent, mark, c.Node().Type(), shortId(c.ID()))
			if e != nil {
				fmt.Printf("%s     %v\n", indent, e)
			}
		})
		fmt.Printf("  %d/%d claims valid\n", count-failed, count)
		if err != nil {
			totalFailed++
		}
	}
	if totalFailed > 0 {
		return fmt.Errorf("validate: %d branch(es) failed", totalFailed)
	}
	return nil
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
	if n.ContentHash() != nil {
		fmt.Printf("content_hash: %s\n", n.ContentHash().String())
		if b, err := n.Content(); err == nil && b != nil {
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
			if eb := e.Content(); len(eb) > 0 {
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
