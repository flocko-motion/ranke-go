package ranke

import (
	"context"
	"testing"
)

// MergeClosure should pull every claim + content from src into dst,
// be idempotent on re-run, and surface ErrNotFound when the head is
// missing in src.
func TestMergeClosure(t *testing.T) {
	ctx := context.Background()

	// Build a small graph in srcArchive (uses src Universe under the hood).
	src := NewMemUniverse()
	dst := NewMemUniverse()
	srcArc, err := NewArchive(ctx, src, NewMemBranchTableHead())
	if err != nil {
		t.Fatalf("NewArchive(src): %v", err)
	}

	operator, err := ClaimBuilder{
		Type:    NodeContributor,
		Content: []byte("op@example.com"),
	}.Sign()
	if err != nil {
		t.Fatalf("contributor: %v", err)
	}
	op, err := operator.AsContributor()
	if err != nil {
		t.Fatalf("AsContributor: %v", err)
	}

	em, err := ClaimBuilder{
		Type:        TypeSource("email"),
		Encoding:    EncodingMessage("rfc822"),
		Content:     []byte("From: a\r\nTo: b\r\n\r\nhi\r\n"),
		Contributor: op,
	}.Sign()
	if err != nil {
		t.Fatalf("email: %v", err)
	}

	g := NewGraph(op)
	if err := g.Add(em); err != nil {
		t.Fatalf("g.Add: %v", err)
	}
	if err := srcArc.AddGraph(ctx, "main", g, op); err != nil {
		t.Fatalf("AddGraph: %v", err)
	}

	srcBranch, err := srcArc.GetBranch(ctx, "main")
	if err != nil {
		t.Fatalf("GetBranch: %v", err)
	}
	head := srcBranch.Latest().Head()

	// Merge into dst.
	if err := dst.MergeClosure(ctx, src, head); err != nil {
		t.Fatalf("MergeClosure: %v", err)
	}

	// Open an Archive over dst with a fresh BranchTableHead, then
	// walk the closure ourselves via GetClosure on the merged head.
	dstArc, err := NewArchive(ctx, dst, NewMemBranchTableHead())
	if err != nil {
		t.Fatalf("NewArchive(dst): %v", err)
	}
	merged, err := dstArc.GetClosure(ctx, head)
	if err != nil {
		t.Fatalf("dst GetClosure: %v", err)
	}
	for _, want := range []Id{head, em.ID(), op.ID()} {
		if !merged.Contains(want) {
			t.Errorf("dst closure missing %s", want.String())
		}
	}
	if err := merged.Validate(); err != nil {
		t.Errorf("merged closure does not validate: %v", err)
	}

	// Idempotency: running MergeClosure again should be a no-op
	// (dst.HasClaim short-circuits) and must not error.
	if err := dst.MergeClosure(ctx, src, head); err != nil {
		t.Fatalf("MergeClosure (rerun): %v", err)
	}

	// Missing head in src surfaces as an error. Build a valid id
	// that was never saved.
	orphan, err := ClaimBuilder{
		Type:    NodeContributor,
		Content: []byte("never-saved"),
	}.Sign()
	if err != nil {
		t.Fatalf("synthesize orphan: %v", err)
	}
	if err := dst.MergeClosure(ctx, src, orphan.ID()); err == nil {
		t.Errorf("MergeClosure with missing head should error")
	}
}
