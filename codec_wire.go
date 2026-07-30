// package: ranke / codec_wire
// type:    io
// job:     the contribution codec — a CBOR sequence (RFC 8742) opening with the branches it touches,
// then claims under their id and externalized content under its hash
// limits:  the record level only; the claim and node records it carries are codec.go's, and
// admitting them into an archive is the Sequencer's (-> sequencer, contribution)
package ranke

import (
	"io"
	"slices"

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
	// WireHeader is [2, [branch, ...]] — the branches this contribution touches,
	// first in the stream so a reader learns them without draining it.
	WireHeader WireKind = 2
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
	w        io.Writer
	branches []string
	headed   bool
}

// NewWireWriter writes a contribution touching branches. They head the stream, so
// a reader can check them before taking the rest.
func NewWireWriter(w io.Writer, branches ...string) *WireWriter {
	return &WireWriter{w: w, branches: branches}
}

// WriteClaim appends a claim joining branch, as the canonical record under its id —
// the bytes the id was signed over, so the receiver can verify against it.
func (ww *WireWriter) WriteClaim(branch string, c Claim) error {
	if c == nil || c.ID() == nil {
		return errNilClaim
	}
	if branch == "" {
		return ErrWireNoBranch
	}
	if !slices.Contains(ww.branches, branch) {
		return WithDetail(ErrWireUndeclared, branch)
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

// write emits the header once, then marshals the record onto the stream.
func (ww *WireWriter) write(rec []any) error {
	if !ww.headed {
		ww.headed = true
		branches := ww.branches
		if branches == nil {
			branches = []string{} // content alone touches no branch, and still says so
		}
		if err := ww.marshal([]any{uint64(WireHeader), branches}); err != nil {
			return err
		}
	}
	return ww.marshal(rec)
}

// marshal writes one record's canonical bytes.
func (ww *WireWriter) marshal(rec []any) error {
	b, err := encodingMode.Marshal(rec)
	if err != nil {
		return Wrap(ErrWire, err)
	}
	if _, err := ww.w.Write(b); err != nil {
		return Wrap(ErrWire, err)
	}
	return nil
}

// WireReader streams a contribution, decoding one record at a time.
type WireReader struct {
	dec      *cbor.Decoder
	rec      WireRecord
	err      error
	branches []string
	headed   bool
}

// NewWireReader reads a contribution stream from r.
func NewWireReader(r io.Reader) *WireReader {
	return &WireReader{dec: cbor.NewDecoder(r)}
}

// Branches are the branches the stream declares, read from its first record alone —
// so a caller decides whether to take the contribution before reading it.
func (wr *WireReader) Branches() ([]string, error) {
	if err := wr.readHeader(); err != nil {
		return nil, err
	}
	return slices.Clone(wr.branches), nil
}

// readHeader consumes the leading header record, once.
func (wr *WireReader) readHeader() error {
	if wr.headed {
		return wr.err
	}
	wr.headed = true
	raw, ok, err := wr.decode()
	switch {
	case err != nil:
		wr.err = err
		return err
	case !ok:
		return nil // an empty stream carries nothing and declares nothing
	}
	kind, err := wireKind(raw)
	if err != nil {
		wr.err = err
		return err
	}
	if kind != WireHeader || len(raw) < 2 {
		wr.err = ErrWireNoHeader
		return wr.err
	}
	if err := cbor.Unmarshal(raw[1], &wr.branches); err != nil {
		wr.err = Wrap(ErrWire, err)
	}
	return wr.err
}

// Next decodes the next record, returning false at end of stream or on error.
// The kinds differ in arity, so the elements are taken raw and read per kind.
func (wr *WireReader) Next() bool {
	if wr.readHeader() != nil {
		return false
	}
	raw, ok, err := wr.decode()
	switch {
	case err != nil:
		return wr.fail(err)
	case !ok:
		return false
	}
	kind, err := wireKind(raw)
	if err != nil {
		return wr.fail(err)
	}
	if len(raw) < 3 {
		return wr.fail(WithDetail(ErrWire, "record has fewer than three elements"))
	}
	key, payload, ok := wr.keyPayload(raw)
	if !ok {
		return false
	}
	switch kind {
	case WireClaim:
		return wr.readClaim(raw, key, payload)
	case WireContent:
		return wr.readContent(key, payload)
	default:
		return wr.fail(WithDetail(ErrWireKind, key.String()))
	}
}

// decode reads the next record's raw elements; ok is false at end of stream.
func (wr *WireReader) decode() (raw []cbor.RawMessage, ok bool, err error) {
	switch e := wr.dec.Decode(&raw); {
	case e == io.EOF:
		return nil, false, nil
	case e != nil:
		return nil, false, Wrap(ErrWire, e)
	}
	return raw, true, nil
}

// wireKind reads a record's leading kind tag.
func wireKind(raw []cbor.RawMessage) (WireKind, error) {
	if len(raw) == 0 {
		return 0, WithDetail(ErrWire, "record is empty")
	}
	var kind uint64
	if err := cbor.Unmarshal(raw[0], &kind); err != nil {
		return 0, Wrap(ErrWire, err)
	}
	return WireKind(kind), nil
}

// keyPayload reads a record's addressing key and its payload bytes.
func (wr *WireReader) keyPayload(raw []cbor.RawMessage) (Id, []byte, bool) {
	var keyBytes, payload []byte
	if err := cbor.Unmarshal(raw[1], &keyBytes); err != nil {
		return nil, nil, wr.fail(Wrap(ErrWire, err))
	}
	if err := cbor.Unmarshal(raw[2], &payload); err != nil {
		return nil, nil, wr.fail(Wrap(ErrWire, err))
	}
	key, err := idFromBytes(keyBytes)
	if err != nil {
		return nil, nil, wr.fail(Wrap(ErrWire, err))
	}
	return key, payload, true
}

// readClaim decodes a claim record. Its branch must be one the header declared, so
// the header binds the whole stream and a reader can trust what it checked.
func (wr *WireReader) readClaim(raw []cbor.RawMessage, key Id, payload []byte) bool {
	if len(raw) < 4 {
		return wr.fail(WithDetail(ErrWireNoBranch, key.String()))
	}
	var branch string
	if err := cbor.Unmarshal(raw[3], &branch); err != nil {
		return wr.fail(Wrap(ErrWire, err))
	}
	if branch == "" {
		return wr.fail(WithDetail(ErrWireNoBranch, key.String()))
	}
	if !slices.Contains(wr.branches, branch) {
		return wr.fail(WithDetail(ErrWireUndeclared, branch))
	}
	c, err := DecodeClaim(key, payload)
	if err != nil {
		return wr.fail(Wrap(ErrWire, err))
	}
	wr.rec = WireRecord{Kind: WireClaim, Claim: c, Branch: branch}
	return true
}

// readContent decodes a content record, whose bytes must hash to the key it names.
func (wr *WireReader) readContent(key Id, payload []byte) bool {
	sum, err := HashContent(payload)
	if err != nil {
		return wr.fail(Wrap(ErrWire, err))
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
