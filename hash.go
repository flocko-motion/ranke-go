package ranke

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

// hashFromBytes wraps raw multihash bytes and pre-computes the
// multibase string representation. Returns an error if the bytes do
// not parse as a valid multihash.
func hashFromBytes(raw []byte) (*hash, error) {
	if _, err := multihash.Decode(raw); err != nil {
		return nil, fmt.Errorf("invalid multihash: %w", err)
	}
	str, err := multibase.Encode(multibase.Base32, raw)
	if err != nil {
		return nil, fmt.Errorf("multibase encode: %w", err)
	}
	return &hash{raw: raw, str: str}, nil
}

// hashContent returns H(content) — the multihash of the given bytes
// using SHA2-256, wrapped as an *hash. Used for ContentHash on nodes
// and edges, and for record ids (over canonical CBOR).
func hashContent(content []byte) (*hash, error) {
	mh, err := multihash.Sum(content, multihash.SHA2_256, -1)
	if err != nil {
		return nil, fmt.Errorf("multihash sum: %w", err)
	}
	return hashFromBytes(mh)
}

// ParseId parses a multibase-encoded id string into an Id.
func ParseId(s string) (Id, error) {
	_, raw, err := multibase.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("multibase decode: %w", err)
	}
	return hashFromBytes(raw)
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

// Algorithm returns the human-readable name of the hash algorithm
// used to build this id (e.g. "sha2-256").
func (h *hash) Algorithm() string {
	dec, err := multihash.Decode(h.raw)
	if err != nil {
		return "unknown"
	}
	return dec.Name
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
