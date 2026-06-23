// package: ranke / id
// type:    crypto
// job:     the content-addressed Id type — a self-describing payload (multihash or signature) with parsing and equality
// limits:  does not sign or verify ids (-> sign); does not verify content bytes (-> content)
package ranke

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-multihash"
)

// Id is a self-describing, content-addressed identifier: id(v) =
// Sign(H(S(v))) for nodes, id(e) = H(S(e)) for edges (spec §4). Opaque
// to callers.
type Id interface {
	String() string
	Equal(other Id) bool
	// Algorithm names the scheme that built this id, e.g. "sha2-256"
	// or "ed25519-pub".
	Algorithm() string
}

// id is the concrete Id: a self-describing payload (multihash for
// identity-Sign ids, multikey signature for signed ids) with its
// multibase string form cached. A hash is one payload kind, not the
// identity itself.
type id struct {
	raw []byte
	str string // multibase form, cached
}

// idFromBytes wraps a raw payload as an id. The leading varint makes it
// self-describing; no validation here (deferred to verifySignature/Algorithm).
func idFromBytes(raw []byte) (*id, error) {
	str, err := multibase.Encode(multibase.Base32, raw)
	if err != nil {
		return nil, fmt.Errorf("multibase encode: %w", err)
	}
	return &id{raw: raw, str: str}, nil
}

// hashFromMultihashBytes wraps bytes already known to be a multihash,
// validating the framing.
func hashFromMultihashBytes(raw []byte) (*id, error) {
	if _, err := multihash.Decode(raw); err != nil {
		return nil, fmt.Errorf("invalid multihash: %w", err)
	}
	return idFromBytes(raw)
}

// hashContent returns the SHA2-256 multihash of content as an id.
func hashContent(content []byte) (*id, error) {
	mh, err := multihash.Sum(content, multihash.SHA2_256, -1)
	if err != nil {
		return nil, fmt.Errorf("multihash sum: %w", err)
	}
	return hashFromMultihashBytes(mh)
}

// HashContent returns the content-address (SHA2-256 multihash) of bytes,
// for adapters/tooling computing the id content is stored under.
func HashContent(content []byte) (Id, error) {
	return hashContent(content)
}

// ParseId parses a multibase-encoded id string. Accepts both multihash
// and multikey-signature payloads without validating them.
func ParseId(s string) (Id, error) {
	_, raw, err := multibase.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("multibase decode: %w", err)
	}
	return idFromBytes(raw)
}

func (h *id) String() string { return h.str }

// Equal compares by raw payload when both sides are *id, else by string.
func (h *id) Equal(other Id) bool {
	if h == nil || other == nil {
		return h == nil && other == nil
	}
	if o, ok := other.(*id); ok {
		return bytes.Equal(h.raw, o.raw)
	}
	return h.str == other.String()
}

// Algorithm reads the leading multicodec varint to name the scheme
// (hash code, or multikey signature code).
func (h *id) Algorithm() string {
	if dec, err := multihash.Decode(h.raw); err == nil {
		return dec.Name
	}
	if v, n := binary.Uvarint(h.raw); n > 0 {
		return multicodec.Code(v).String()
	}
	return "unknown"
}

// errInvalidId is returned when an Id parameter is required but missing.
var errInvalidId = errors.New("invalid id")
