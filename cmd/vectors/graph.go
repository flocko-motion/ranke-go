// package: main / vectors_graph
// type:    cmd
// job:     the toy graph every implementation must verify — one claim per ADT shape, small enough
// that a reader can check each record against §4.1 by eye
// limits:  valid records only; the rejected ones derive from these (-> broken.go)
package main

import (
	"context"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// externalBlob is the content the external-content claim addresses.
var externalBlob = []byte("externalized content, addressed by hash")

// toyGraph builds the claims that must verify: a contributor, a source note, a
// derived claim citing it, external content, node fields, and a second contributor.
func (g *gen) toyGraph(ctx context.Context) error {
	root, who, err := contributorClaim(ctx, signer("ranke-vectors/root"), epoch)
	if err != nil {
		return err
	}
	g.who = who
	if err := g.addClaim("root-contributor", root,
		"initial node (height 0), content is its own multikey pubkey, signed by that key (§5.7)"); err != nil {
		return err
	}

	note, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a source note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(time.Second)).
		Sign()
	if err != nil {
		return err
	}
	if err := g.addClaim("source-note", note,
		"inline content, one auto contributor edge, height 1"); err != nil {
		return err
	}

	if err := g.derived(who, note); err != nil {
		return err
	}
	if err := g.external(who); err != nil {
		return err
	}
	if err := g.withFields(who); err != nil {
		return err
	}
	return g.secondContributor(ctx)
}

// derived adds a claim citing the note through a derivation edge, so the provenance
// invariant (§3.5) and height = 1 + max(refs) are both exercised.
func (g *gen) derived(who ranke.Contributor, note ranke.Claim) error {
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: note.ID(),
		Type:      ranke.TypeDerivation("note"),
	})
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeDerivation("note"), who).
		WithInlineContent([]byte("derived from the source note")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(e).
		WithHeight(2).
		WithCreatedAt(epoch.Add(2 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("derived-note", c,
		"derivation edge to source-note, height 2 = 1 + max(referenced heights)")
}

// external adds a claim whose content lives outside the record, plus the blob.
func (g *gen) external(who ranke.Contributor) error {
	hash, err := ranke.HashContent(externalBlob)
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("blob"), who).
		WithExternalContent(hash, uint64(len(externalBlob))).
		WithEncoding(ranke.EncodingOctetStream).
		WithHeight(1).
		WithCreatedAt(epoch.Add(3 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	if err := g.addClaim("external-content", c,
		"content_hash + content_size in the record, bytes alongside (§Content)"); err != nil {
		return err
	}
	return g.addContent("external-blob", externalBlob, hash, true, reasonOK,
		"the bytes external-content addresses; hashes to the content_hash it declares")
}

// withFields adds a claim carrying node fields, whose keys the encoder aliases.
func (g *gen) withFields(who ranke.Contributor) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note with fields")).
		WithEncoding(ranke.EncodingPlain).
		WithField("title", "reference vector").
		WithField("lang", "en").
		WithHeight(1).
		WithCreatedAt(epoch.Add(4 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("with-fields", c,
		"node fields under tag 8, key-sorted by the encoder; known keys carry a '.' alias")
}

// secondContributor adds a second identity and a claim of its own, so a case can
// offer one contributor's record under another's signature.
func (g *gen) secondContributor(ctx context.Context) error {
	other, who, err := contributorClaim(ctx, signer("ranke-vectors/other"), epoch.Add(5*time.Second))
	if err != nil {
		return err
	}
	if err := g.addClaim("other-contributor", other,
		"a second identity, so a record can be offered under the wrong signer's id"); err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note by the other contributor")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(6 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	return g.addClaim("other-note", c, "attributed to other-contributor")
}
