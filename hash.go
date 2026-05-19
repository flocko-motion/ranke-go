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

// idFromBytes wraps an opaque self-describing id payload (multihash
// for identity-Sign ids, or multikey signature bytes for signed ids
// per §4.1) and pre-computes the multibase string representation.
// Does not validate the payload — the leading varint code is what
// makes it self-describing, and the dispatch happens at use time
// (verifySignature, Algorithm).
func idFromBytes(raw []byte) (*hash, error) {
	str, err := multibase.Encode(multibase.Base32, raw)
	if err != nil {
		return nil, fmt.Errorf("multibase encode: %w", err)
	}
	return &hash{raw: raw, str: str}, nil
}

// hashFromMultihashBytes wraps bytes already known to be a valid
// multihash (e.g. ContentHash values returned from hashContent).
// Validates the multihash framing.
func hashFromMultihashBytes(raw []byte) (*hash, error) {
	if _, err := multihash.Decode(raw); err != nil {
		return nil, fmt.Errorf("invalid multihash: %w", err)
	}
	return idFromBytes(raw)
}

// hashContent returns H(content) — the multihash of the given bytes
// using SHA2-256, wrapped as an *hash. Used for ContentHash on nodes
// and edges, and as the input to Sign for claim/edge id computation.
func hashContent(content []byte) (*hash, error) {
	mh, err := multihash.Sum(content, multihash.SHA2_256, -1)
	if err != nil {
		return nil, fmt.Errorf("multihash sum: %w", err)
	}
	return hashFromMultihashBytes(mh)
}

// ParseId parses a multibase-encoded id string into an Id. The
// payload may be a multihash (identity-Sign id) or a multikey
// signature (signed id); both are accepted without validation here.
func ParseId(s string) (Id, error) {
	_, raw, err := multibase.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("multibase decode: %w", err)
	}
	return idFromBytes(raw)
}

// String returns the multibase-encoded form (base32 with prefix 'b').
func (h *hash) String() string { return h.str }

// Equal reports whether two ids name the same record. Compares by
// raw multihash bytes when both sides are *hash; falls back to
// string comparison otherwise.
func (h *hash) Equal(other Id) bool {
	if h == nil || other == nil {
		return h == nil && other == nil
	}
	if o, ok := other.(*hash); ok {
		return bytes.Equal(h.raw, o.raw)
	}
	return h.str == other.String()
}

// Algorithm returns the human-readable name of the scheme that
// produced this id — a hash algorithm (e.g. "sha2-256") for an
// identity-Sign id, or a signature scheme (e.g. "ed25519-pub") for
// a signed id. Both encodings carry a leading multicodec varint
// (multihash uses hash codes; multikey signatures use key codes),
// so dispatch is by reading the leading code.
func (h *hash) Algorithm() string {
	if dec, err := multihash.Decode(h.raw); err == nil {
		return dec.Name
	}
	if v, n := binary.Uvarint(h.raw); n > 0 {
		return multicodec.Code(v).String()
	}
	return "unknown"
}

// idsEqual is a small helper used internally to compare two Id
// interface values, including the nil case.
func idsEqual(a, b Id) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(b)
}

// errInvalidId is returned when an Id parameter is required but missing.
var errInvalidId = errors.New("invalid id")
