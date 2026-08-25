// package: ranke / id
// type:    crypto
// job:     the content-addressed Id type — a multihash with parsing and equality
// limits:  does not sign or verify ids (-> sign); does not verify content bytes (-> content)
package ranke

import (
	"bytes"

	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

// Id is a content-addressed identifier, and always a multihash (`V-HASH`):
// id(v) = H(S(env(v))) for a claim (`V-ID`), id(e) = H(S(e)) for an edge, and
// H(c) for external content. The envelope carries the signature that once
// framed a claim's id (`V-ENV`), so one framing now serves all three.
type Id interface {
	String() string
	Equal(other Id) bool
	// Algorithm names the hash that built this id, e.g. "sha2-256".
	Algorithm() string
	// rawBytes returns the multihash payload; unexported, so only this
	// package's *id satisfies Id.
	rawBytes() []byte
}

// id is the concrete Id: a multihash with its multibase string form cached.
type id struct {
	raw []byte
	str string // multibase form, cached
}

// idFromBytes wraps multihash bytes as an id. It validates the framing, so a
// value reaching an Id is one, and a malformed reference fails at the decode
// that read it rather than at the comparison that trusted it.
func idFromBytes(raw []byte) (*id, error) {
	if _, err := multihash.Decode(raw); err != nil {
		return nil, WrapDetail(errID, "invalid multihash", err)
	}
	str, err := multibase.Encode(multibase.Base32, raw)
	if err != nil {
		return nil, WrapDetail(errID, "multibase encode", err)
	}
	return &id{raw: raw, str: str}, nil
}

// hashContent returns the SHA2-256 multihash of content as an id.
func hashContent(content []byte) (*id, error) {
	mh, err := multihash.Sum(content, multihash.SHA2_256, -1)
	if err != nil {
		return nil, WrapDetail(errID, "multihash sum", err)
	}
	return idFromBytes(mh)
}

// HashContent returns the content-address (SHA2-256 multihash) of bytes.
func HashContent(content []byte) (Id, error) {
	return hashContent(content)
}

// ParseId parses a multibase-encoded id string into its multihash.
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

// Algorithm names the hash. Every constructed id decodes, so "unknown" is the
// zero value reaching here rather than a payload of another shape.
func (h *id) Algorithm() string {
	dec, err := multihash.Decode(h.raw)
	if err != nil || dec.Name == "" {
		return "unknown"
	}
	return dec.Name
}
