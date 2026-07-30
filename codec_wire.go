// package: ranke / codec_wire
// type:    io
// job:     the contribution codec — a CBOR sequence (RFC 8742) of records carrying claims under
// their id, with the branch they join, and externalized content under its hash
// limits:  the record level only; the claim and node records it carries are codec.go's, and
// admitting them into an archive is the Sequencer's (-> sequencer, contribution)
package ranke

import (
	"io"

	"github.com/fxamacker/cbor/v2"
)

// WireMediaType is the Content-Type a contribution stream is served under: a CBOR
// sequence is self-framing, so records concatenate with no envelope.
const WireMediaType = "application/cbor-seq"

// WireKind tags what a record carries, so a reader switches once per record.
type WireKind uint64

const (
	// WireClaim is [0, id, canonical claim CBOR, branch] — a claim and the branch
	// it joins, since a contribution may name several.
	WireClaim WireKind = 0
	// WireContent is [1, hash, blob bytes]. Content is addressed by its hash and
	// lives in the Universe unbranched, so it names no branch.
	WireContent WireKind = 1
)

// WireRecord is one decoded record: Kind names which fields carry the payload.
type WireRecord struct {
	Kind   WireKind
	Claim  Claim       // WireClaim: decoded under the id the record names
	Branch string      // WireClaim: the branch this claim joins
	Blob   ContentBlob // WireContent: the bytes, checked against the hash
}

// WireWriter writes a contribution stream. Records concatenate, so a contribution
// of any size streams without being buffered.
type WireWriter struct {
	w io.Writer
}

// NewWireWriter writes records to w.
func NewWireWriter(w io.Writer) *WireWriter { return &WireWriter{w: w} }

// WriteClaim appends a claim joining branch, as the canonical record under its id —
// the bytes the id was signed over, so the receiver can verify against it.
func (ww *WireWriter) WriteClaim(branch string, c Claim) error {
	if c == nil || c.ID() == nil {
		return errNilClaim
	}
	if branch == "" {
		return errWireNoBranch
	}
	raw, err := c.EncodeCBOR(FormOriginal)
	if err != nil {
		return err
	}
	return ww.write([]any{uint64(WireClaim), idBytes(c.ID()), raw, branch})
}

// WriteContent appends externalized content under its hash.
func (ww *WireWriter) WriteContent(b ContentBlob) error {
	if b.Hash == nil {
		return errNilHash
	}
	return ww.write([]any{uint64(WireContent), idBytes(b.Hash), b.Content})
}

// write marshals one record and appends it to the stream.
func (ww *WireWriter) write(rec []any) error {
	b, err := encodingMode.Marshal(rec)
	if err != nil {
		return Wrap(errWire, err)
	}
	if _, err := ww.w.Write(b); err != nil {
		return Wrap(errWire, err)
	}
	return nil
}

// WireReader streams a contribution, decoding one record at a time.
type WireReader struct {
	dec *cbor.Decoder
	rec WireRecord
	err error
}

// NewWireReader reads a contribution stream from r.
func NewWireReader(r io.Reader) *WireReader {
	return &WireReader{dec: cbor.NewDecoder(r)}
}

// Next decodes the next record, returning false at end of stream or on error.
// The two kinds differ in arity, so the elements are taken raw and read per kind.
func (wr *WireReader) Next() bool {
	if wr.err != nil {
		return false
	}
	var raw []cbor.RawMessage
	switch err := wr.dec.Decode(&raw); {
	case err == io.EOF:
		return false
	case err != nil:
		return wr.fail(Wrap(errWire, err))
	}
	if len(raw) < 3 {
		return wr.fail(WithDetail(errWire, "record has fewer than three elements"))
	}
	var kind uint64
	if err := cbor.Unmarshal(raw[0], &kind); err != nil {
		return wr.fail(Wrap(errWire, err))
	}
	key, payload, ok := wr.keyPayload(raw)
	if !ok {
		return false
	}
	switch WireKind(kind) {
	case WireClaim:
		return wr.readClaim(raw, key, payload)
	case WireContent:
		return wr.readContent(key, payload)
	default:
		return wr.fail(WithDetail(errWireKind, key.String()))
	}
}

// keyPayload reads a record's addressing key and its payload bytes.
func (wr *WireReader) keyPayload(raw []cbor.RawMessage) (Id, []byte, bool) {
	var keyBytes, payload []byte
	if err := cbor.Unmarshal(raw[1], &keyBytes); err != nil {
		return nil, nil, wr.fail(Wrap(errWire, err))
	}
	if err := cbor.Unmarshal(raw[2], &payload); err != nil {
		return nil, nil, wr.fail(Wrap(errWire, err))
	}
	key, err := idFromBytes(keyBytes)
	if err != nil {
		return nil, nil, wr.fail(Wrap(errWire, err))
	}
	return key, payload, true
}

// readClaim decodes a claim record, which names the branch it joins.
func (wr *WireReader) readClaim(raw []cbor.RawMessage, key Id, payload []byte) bool {
	if len(raw) < 4 {
		return wr.fail(WithDetail(errWireNoBranch, key.String()))
	}
	var branch string
	if err := cbor.Unmarshal(raw[3], &branch); err != nil {
		return wr.fail(Wrap(errWire, err))
	}
	if branch == "" {
		return wr.fail(WithDetail(errWireNoBranch, key.String()))
	}
	c, err := DecodeClaim(key, payload)
	if err != nil {
		return wr.fail(Wrap(errWire, err))
	}
	wr.rec = WireRecord{Kind: WireClaim, Claim: c, Branch: branch}
	return true
}

// readContent decodes a content record, whose bytes must hash to the key it names.
func (wr *WireReader) readContent(key Id, payload []byte) bool {
	sum, err := HashContent(payload)
	if err != nil {
		return wr.fail(Wrap(errWire, err))
	}
	if !sum.Equal(key) {
		return wr.fail(WithDetail(ErrIntegrity, "wire content "+key.String()))
	}
	wr.rec = WireRecord{Kind: WireContent, Blob: ContentBlob{Hash: key, Content: payload}}
	return true
}

// fail records the error that stopped the stream and reports "no record".
func (wr *WireReader) fail(err error) bool {
	wr.err = err
	return false
}

// Record returns the record Next decoded.
func (wr *WireReader) Record() WireRecord { return wr.rec }

// Err returns the first error that stopped the stream.
func (wr *WireReader) Err() error { return wr.err }
