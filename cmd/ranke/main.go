// ranke is a small CLI for inspecting filesystem-backed Ranke-Graph
// archives. It opens an archive at <dir> and runs read-only queries
// against it. No mutation commands yet — building claims live in
// the test suite and downstream apps.
//
// Subcommands:
//
//	ranke info <dir>            — high-level summary (B_h, branches)
//	ranke branches <dir>        — list every branch and its head id
//	ranke show <dir> <id>       — decode and print one claim
//	ranke validate <dir> <head> — fetch the graph rooted at head and run Validate
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/flocko-motion/ranke-go"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "info":
		exit(cmdInfo(args))
	case "branches":
		exit(cmdBranches(args))
	case "show":
		exit(cmdShow(args))
	case "validate":
		exit(cmdValidate(args))
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
  ranke show     <dir> <id>
  ranke validate <dir> <head>

dir is a filesystem-backed archive (see NewFsArchive). Read-only.`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ranke:", err)
		os.Exit(1)
	}
}

func openArchive(dir string) (ranke.Archive, error) {
	a, err := ranke.NewFsArchive(dir)
	if err != nil {
		return nil, fmt.Errorf("open archive %s: %w", dir, err)
	}
	return a, nil
}

// --- info ---

func cmdInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("info: usage: ranke info <dir>")
	}
	a, err := openArchive(args[0])
	if err != nil {
		return err
	}
	branches := a.Branches()
	fmt.Printf("archive %s\n", args[0])
	fmt.Printf("  branches: %d\n", len(branches))
	for _, b := range branches {
		fmt.Printf("    %s → %s\n", b.Name(), shortId(b.Latest().Head()))
	}
	return nil
}

// --- branches ---

func cmdBranches(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("branches: usage: ranke branches <dir>")
	}
	a, err := openArchive(args[0])
	if err != nil {
		return err
	}
	bs := a.Branches()
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

func cmdShow(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("show: usage: ranke show <dir> <id>")
	}
	a, err := openArchive(args[0])
	if err != nil {
		return err
	}
	id, err := ranke.ParseId(args[1])
	if err != nil {
		return fmt.Errorf("show: parse id: %w", err)
	}
	g, err := a.GetGraph(id)
	if err != nil {
		return fmt.Errorf("show: fetch claim: %w", err)
	}
	c, ok := g.GetClaim(id)
	if !ok {
		return fmt.Errorf("show: claim %s not in graph", id.String())
	}
	printClaim(c)
	return nil
}

// --- validate ---

func cmdValidate(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("validate: usage: ranke validate <dir> <head>")
	}
	a, err := openArchive(args[0])
	if err != nil {
		return err
	}
	id, err := ranke.ParseId(args[1])
	if err != nil {
		return fmt.Errorf("validate: parse head: %w", err)
	}
	g, err := a.GetGraph(id)
	if err != nil {
		return fmt.Errorf("validate: fetch graph: %w", err)
	}
	if err := g.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	fmt.Printf("ok — %s validates\n", shortId(id))
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
			if eb, err := e.Content(); err == nil && eb != nil {
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
