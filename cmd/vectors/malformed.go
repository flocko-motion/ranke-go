// package: main / vectors_malformed
// type:    cmd
// job:     the records no builder here will produce — a timestamp outside the form `V-TIME` fixes,
// and a record carrying both content slots `V-CONTENT` forbids — each offered under an id that
// holds for its bytes, so the named defect is the only one
// limits:  patches the CBOR of a record this program built (-> graph.go); what the rule requires is
// read from the paper, never from what this repo's decoder happens to do
package main

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/internal/vectors"
	"github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-multihash"
)

// errIDFraming fires when the id this file derives disagrees with the library's over
// the same bytes, which would make every patched record's id wrong in the same way.
var errIDFraming = errors.New("cmd/vectors: re-derived id does not match the library's")

// Record keys `V-SER` fixes, the ones these cases reach for.
const (
	keyNode        = 1
	keyContentHash = 5
	keyCreatedAt   = 9
)

// noNanoCreatedAt is epoch in plain RFC 3339 — the near-miss `V-TIME` exists to
// outlaw, and the form this repo's own generator once wrote. A reader that accepts
// it reads one instant in two forms.
const noNanoCreatedAt = "2023-11-14T22:13:20Z"

// unparsableTime is a value no reader can take for a timestamp at all.
const unparsableTime = "whenever"

// malformed writes the records that break `V-TIME` and `V-CONTENT`. The two field
// cases go through the builder, which does not judge a field's value, so their only
// defect is the timestamp; the other two are patched bytes, since no encoder here
// emits them.
func (g *gen) malformed() error {
	if err := g.badFieldTime("rejected-delete-by-form", ranke.FieldDeleteBy,
		"delete_by is not RFC 3339: a deletion due date no reader can place in time"); err != nil {
		return err
	}
	if err := g.badFieldTime("rejected-pubkey-window-form", ranke.FieldPubkeyExpiresAfter,
		"pubkey_expires_after is not RFC 3339, so the key window it states cannot be read"); err != nil {
		return err
	}
	if err := g.badCreatedAt(); err != nil {
		return err
	}
	return g.bothContentSlots()
}

// badFieldTime signs a record whose field carries an unparsable timestamp. The
// builder does not parse field values, so the signature holds over these very bytes
// and the timestamp is the whole defect.
func (g *gen) badFieldTime(name, field, why string) error {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent([]byte("a note carrying "+field+"="+unparsableTime)).
		WithEncoding(ranke.EncodingPlain).
		WithField(field, unparsableTime).
		WithHeight(1).
		WithCreatedAt(epoch.Add(10 * time.Second)).
		Sign()
	if err != nil {
		return err
	}
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return err
	}
	return g.addBroken(name, raw, c.ID().String(), vectors.ReasonTimestampForm, why, "V-TIME")
}

// badCreatedAt rewrites created_at to a form that drops the nanoseconds, which is
// the near-miss a reader is likeliest to accept.
func (g *gen) badCreatedAt() error {
	raw, id, err := g.patchedRecord([]byte("a note whose created_at drops its nanoseconds"),
		func(node map[uint64]cbor.RawMessage) error {
			enc, err := ranke.MarshalCBOR(noNanoCreatedAt)
			if err != nil {
				return err
			}
			node[keyCreatedAt] = enc
			return nil
		})
	if err != nil {
		return err
	}
	return g.addBroken("rejected-created-at-form", raw, id.String(), vectors.ReasonTimestampForm,
		"created_at is RFC 3339 without the nanosecond fraction the rule fixes", "V-TIME")
}

// bothContentSlots adds content_hash to a record that already carries content, the
// malformed shape another implementation might store.
func (g *gen) bothContentSlots() error {
	raw, id, err := g.patchedRecord([]byte("a note in both content slots at once"),
		func(node map[uint64]cbor.RawMessage) error {
			addr, err := ranke.HashContent(externalBlob)
			if err != nil {
				return err
			}
			// A record holds the multihash unwrapped, which is what an Id's multibase
			// string form decodes back to.
			_, hash, err := multibase.Decode(addr.String())
			if err != nil {
				return err
			}
			enc, err := ranke.MarshalCBOR(hash)
			if err != nil {
				return err
			}
			node[keyContentHash] = enc
			return nil
		})
	if err != nil {
		return err
	}
	return g.addBroken("rejected-both-content-slots", raw, id.String(), vectors.ReasonBothContent,
		"one record carrying content and content_hash, which the rule makes exclusive", "V-CONTENT")
}

// patchedRecord builds a claim the root identity signs, patches its node record, and
// re-signs the result. Re-signing is what leaves ONE defect: an id that no longer
// held for the bytes would be a second, and the case could then be rejected for it.
func (g *gen) patchedRecord(content []byte, patch func(map[uint64]cbor.RawMessage) error) ([]byte, ranke.Id, error) {
	c, err := ranke.NewClaim(ranke.TypeSource("note"), g.who).
		WithInlineContent(content).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(epoch.Add(11 * time.Second)).
		Sign()
	if err != nil {
		return nil, nil, err
	}
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return nil, nil, err
	}
	// The framing below is re-derived here, so it is proven against the record whose
	// id the library just produced: same bytes, same id, or the framing is wrong.
	unpatched, err := patchNode(raw, func(map[uint64]cbor.RawMessage) error { return nil })
	if err != nil {
		return nil, nil, err
	}
	check, err := signNode(unpatched.node)
	if err != nil {
		return nil, nil, err
	}
	if !check.Equal(c.ID()) {
		return nil, nil, errIDFraming
	}
	patched, err := patchNode(raw, patch)
	if err != nil {
		return nil, nil, err
	}
	id, err := signNode(patched.node)
	if err != nil {
		return nil, nil, err
	}
	return patched.file, id, nil
}

// signNode derives id(v) = Sign(H(S(v))) for a node record this program assembled:
// the sha2-256 multihash `V-HASH` fixes, signed with Ed25519 and framed under the
// `eddsa` multicodec `V-SIGN` names. The library's own signer takes a claim, not bytes.
func signNode(node []byte) (ranke.Id, error) {
	hash, err := multihash.Sum(node, multihash.SHA2_256, -1)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(signer(rootSeed), hash)
	payload := append(binary.AppendUvarint(nil, uint64(multicodec.Eddsa)), sig...)
	s, err := multibase.Encode(multibase.Base32, payload)
	if err != nil {
		return nil, err
	}
	return ranke.ParseId(s)
}

// record is a claim file beside the node record inside it, which is what an id is
// computed over.
type record struct{ file, node []byte }

// patchNode rewrites a claim record's node map and re-encodes it. Every key it does
// not touch is carried through as raw bytes, so the patch is the only difference.
func patchNode(raw []byte, patch func(map[uint64]cbor.RawMessage) error) (record, error) {
	var file map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(raw, &file); err != nil {
		return record{}, err
	}
	var node map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(file[keyNode], &node); err != nil {
		return record{}, err
	}
	if err := patch(node); err != nil {
		return record{}, err
	}
	encoded, err := ranke.MarshalCBOR(node)
	if err != nil {
		return record{}, err
	}
	file[keyNode] = encoded
	out, err := ranke.MarshalCBOR(file)
	if err != nil {
		return record{}, err
	}
	return record{file: out, node: encoded}, nil
}
