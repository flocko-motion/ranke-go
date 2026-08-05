// package: ranke / id
// type:    crypto
// job:     the content-addressed Id type — a self-describing payload (multihash or signature) with
// parsing and equality
// limits:  does not sign or verify ids (-> sign); does not verify content bytes (-> content)
package ranke

import (
	"bytes"
	"encoding/binary"

	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-multihash"
)

// Id is a self-describing, content-addressed identifier: id(v) =
// Sign(H(S(v))) for nodes, id(e) = H(S(e)) for edges (spec §4).
type Id interface {
	String() string
	Equal(other Id) bool
	// Algorithm names the scheme that built this id, e.g. "sha2-256".
	Algorithm() string
	// rawBytes returns the self-describing payload; unexported, so only
	// this package's *id satisfies Id.
	rawBytes() []byte
}

// id is the concrete Id: a self-describing payload — multihash, or multikey
// signature for signed ids — with its multibase string form cached.
type id struct {
	raw []byte
	str string // multibase form, cached
}

// idFromBytes wraps a raw payload as an id, its leading varint self-describing.
func idFromBytes(raw []byte) (*id, error) {
	str, err := multibase.Encode(multibase.Base32, raw)
	if err != nil {
		return nil, WrapDetail(errID, "multibase encode", err)
	}
	return &id{raw: raw, str: str}, nil
}

// hashFromMultihashBytes wraps bytes already known to be a multihash,
// validating the framing.
func hashFromMultihashBytes(raw []byte) (*id, error) {
	if _, err := multihash.Decode(raw); err != nil {
		return nil, WrapDetail(errID, "invalid multihash", err)
	}
	return idFromBytes(raw)
}

// hashContent returns the SHA2-256 multihash of content as an id.
func hashContent(content []byte) (*id, error) {
	mh, err := multihash.Sum(content, multihash.SHA2_256, -1)
	if err != nil {
		return nil, WrapDetail(errID, "multihash sum", err)
	}
	return hashFromMultihashBytes(mh)
}

// HashContent returns the content-address (SHA2-256 multihash) of bytes.
func HashContent(content []byte) (Id, error) {
	return hashContent(content)
}

// ParseId parses a multibase-encoded id string, multihash or signature payload.
func ParseId(s string) (Id, error) {
	_, raw, err := multibase.Decode(s)
	if err != nil {
		return nil, WrapDetail(errID, "multibase decode", err)
	}
	return idFromBytes(raw)
}

func (h *id) String() string { return h.str }

func (h *id) rawBytes() []byte {
	if h == nil {
		return nil
	}
	return h.raw
}

// Equal compares by raw payload.
func (h *id) Equal(other Id) bool {
	if h == nil || other == nil {
		return h == nil && other == nil
	}
	return bytes.Equal(h.raw, other.rawBytes())
}

// Algorithm reads the leading multicodec varint to name the scheme.
//
// A named multihash is what counts as one. multihash.Decode reads a code and then a
// length, and a signature id supplies both by coincidence: its multikey code is 0xed
// 0x01, so the length it reads is the signature's FIRST BYTE, and one signature in 256
// begins with the 63 that matches the bytes remaining. Such an id decoded as an
// unnamed multihash and this returned "" for it.
func (h *id) Algorithm() string {
	if dec, err := multihash.Decode(h.raw); err == nil && dec.Name != "" {
		return dec.Name
	}
	if v, n := binary.Uvarint(h.raw); n > 0 {
		return multicodec.Code(v).String()
	}
	return "unknown"
}
