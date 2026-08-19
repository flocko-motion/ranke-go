// package: main / vectors_broken
// type:    cmd
// job:     the records every implementation must reject, each isolating one failure — a wrong id, a
// bare hash where a signature belongs, an unresolvable contributor, a declared height that is not
// the derived one, content that misses its hash
// limits:  derives from the toy graph (-> graph.go); each case breaks one thing, so a rejection
// names a cause
package main

import (
	"context"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/internal/vectors"
	"github.com/fxamacker/cbor/v2"
)

// broken writes the rejected cases, each pairing bytes with an id that does not
// hold for them, or a record whose closure cannot answer for it.
func (g *gen) broken(ctx context.Context) error {
	note, noteID := g.raw["source-note"], g.ids["source-note"]

	if err := g.addBroken("rejected-wrong-message", note, g.ids["derived-note"].String(),
		vectors.ReasonWrongMessage,
		"source-note's record under derived-note's id: a real signature over a different record",
		"V-ID"); err != nil {
		return err
	}
	if err := g.addBroken("rejected-wrong-signer", g.raw["other-note"], g.ids["source-note"].String(),
		vectors.ReasonWrongMessage,
		"other-note's record under an id signed by the root key, which never signed it",
		"V-ID"); err != nil {
		return err
	}
	if err := g.identitySign(note, noteID); err != nil {
		return err
	}
	if err := g.malformedID(note, noteID); err != nil {
		return err
	}
	if err := g.tamperedContent(noteID); err != nil {
		return err
	}
	if err := g.wrongHeight(); err != nil {
		return err
	}
	if err := g.unresolvableContributor(ctx); err != nil {
		return err
	}
	return g.tamperedBlob()
}

// identitySign offers a record under H(S(node)) — the identity-Sign form (§4.1) —
// though its contributor publishes a pubkey, so a signature is required.
func (g *gen) identitySign(raw []byte, id ranke.Id) error {
	var rf struct {
		Node cbor.RawMessage `cbor:"1,keyasint"`
	}
	if err := cbor.Unmarshal(raw, &rf); err != nil {
		return err
	}
	hash, err := ranke.HashContent(rf.Node)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-identity-sign", raw, hash.String(), vectors.ReasonIdentitySign,
		"the bare H(S(node)) as id, valid only where the contributor publishes no key",
		"V-SIG")
}

// malformedID truncates the id's multibase payload, so it fails before any hashing.
func (g *gen) malformedID(raw []byte, id ranke.Id) error {
	s := id.String()
	return g.addBroken("rejected-malformed-id", raw, s[:len(s)-6], vectors.ReasonMalformedID,
		"the id's payload is truncated, so its multikey framing no longer parses",
		"V-SIGN")
}

// tamperedContent restates the note with different content under the original id,
// the shape a modified archive takes.
func (g *gen) tamperedContent(id ranke.Id) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a tampered note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-tampered-content", raw, id.String(), vectors.ReasonIDMismatch,
		"source-note with its content changed, still offered under source-note's id",
		"V-ID")
}

// wrongHeight declares a height the edge set does not derive, which the verifier
// re-derives and enforces even though the signature holds.
func (g *gen) wrongHeight() error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a note claiming the wrong height")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(7).
		WithCreatedAt(epoch.Add(7 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-wrong-height", raw, c.ID().String(), vectors.ReasonHeightWrong,
		"height 7 where the only reference is a height-0 contributor, so 1 is derived",
		"V-HEIGHT")
}

// unresolvableContributor attributes a claim to an identity absent from the set, so
// no pubkey answers for the signature.
func (g *gen) unresolvableContributor(ctx context.Context) error {
	_, who, err := contributorClaim(ctx, signer("ranke-vectors/absent"), epoch.Add(8*time.Second))
	if err != nil {
		return err
	}
	c, err := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte("a note by an identity not in this set")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(9 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return err
	}
	return g.addBroken("rejected-absent-contributor", raw, c.ID().String(), vectors.ReasonNoContributor,
		"its contributor edge names a claim this set omits, so the pubkey cannot be resolved",
		"V-REF", "V-SIG")
}

// tamperedBlob offers different bytes under the external content's hash.
func (g *gen) tamperedBlob() error {
	hash, err := ranke.HashContent(externalBlob)
	if err != nil {
		return err
	}
	return g.addContent("rejected-external-blob", []byte("tampered content, same declared hash"),
		hash, false, vectors.ReasonContentMismatch,
		"bytes that do not hash to the content_hash external-content declares",
		"V-CONTENT")
}
