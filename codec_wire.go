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
	"strconv"

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
	// WireBranches is [2, [branch, ...]] — the branches this contribution writes to.
	WireBranches WireKind = 2
	// WireReferencable is [3, [branch, ...]] — the branches, $archive or $universe it
	// may reference claims from.
	WireReferencable WireKind = 3
	// WireLifted is [4, [type, ...]] — the reserved node types it asks to create.
	WireLifted WireKind = 4
	// WireCreatable is [5, [branch, ...]] — the branches it may bring into being, a
	// branch the base does not carry being created by the merge that names it.
	WireCreatable WireKind = 5
)

// WireConstraints are the constraints a stream declares, one record each, all of them
// ahead of the payload so a receiver reads them before taking anything.
//
// Branches and Referencable narrow: declaring them can only reduce what the
// contribution does. Lifted widens, so a receiver relaying a stream from a party it
// does not trust checks that record against what that party may ask for.
type WireConstraints struct {
	Branches     []string // record 2, required
	Referencable []string // record 3, required
	Lifted       []string // record 4, empty asks for nothing
	Creatable    []string // record 5, empty asks for nothing
}

// Narrow returns the constraints both w and o permit. Declaring restricts, so two
// declarations compose to their overlap and a relay adds its own limits by merging
// rather than by appending a second record.
func (w WireConstraints) Narrow(o WireConstraints) WireConstraints {
	return WireConstraints{
		Branches:     intersect(w.Branches, o.Branches),
		Referencable: narrowScopes(w.Referencable, o.Referencable),
		Lifted:       intersect(w.Lifted, o.Lifted),
		Creatable:    intersect(w.Creatable, o.Creatable),
	}
}

// narrowScopes intersects read scopes under the containment $universe ⊇ $archive ⊇ any
// branch, so naming a branch alongside a wider scope leaves the branch.
func narrowScopes(a, b []string) []string {
	// Widest first, so naming $archive opposite $universe leaves $archive.
	for _, wide := range []string{BranchUniverse, BranchArchive} {
		switch {
		case slices.Contains(a, wide):
			return slices.Clone(b)
		case slices.Contains(b, wide):
			return slices.Clone(a)
		}
	}
	return intersect(a, b)
}

// intersect returns the entries both lists carry, in a's order.
func intersect(a, b []string) []string {
	out := make([]string, 0, min(len(a), len(b)))
	for _, s := range a {
		if slices.Contains(b, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Options renders the declarations as the options the contribution opens under.
func (w WireConstraints) Options() []ContributionOption {
	opts := []ContributionOption{WithReferencableBranches(w.Referencable...)}
	if len(w.Lifted) > 0 {
		opts = append(opts, WithLiftedTypes(w.Lifted...))
	}
	if len(w.Creatable) > 0 {
		opts = append(opts, WithCreatableBranches(w.Creatable...))
	}
	return opts
}

// constraintKind reports whether kind carries a constraint rather than payload.
func constraintKind(kind WireKind) bool {
	switch kind {
	case WireBranches, WireReferencable, WireLifted, WireCreatable:
		return true
	}
	return false
}

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
	w      io.Writer
	cons   WireConstraints
	headed bool
}

// NewWireWriter writes a contribution under cons, which heads the stream so a reader
// checks it before taking the rest.
func NewWireWriter(w io.Writer, cons WireConstraints) *WireWriter {
	return &WireWriter{w: w, cons: cons}
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
	if !slices.Contains(ww.cons.Branches, branch) {
		return WithDetail(ErrWireUndeclared, branch)
	}
	raw, err := c.Envelope()
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

// write emits the constraint records once, then marshals the record onto the stream.
func (ww *WireWriter) write(rec []any) error {
	if !ww.headed {
		ww.headed = true
		// Both required records are written even when empty: content alone touches no
		// branch and reads from none, and says so.
		if err := ww.marshal([]any{uint64(WireBranches), list(ww.cons.Branches)}); err != nil {
			return err
		}
		if err := ww.marshal([]any{uint64(WireReferencable), list(ww.cons.Referencable)}); err != nil {
			return err
		}
		for _, opt := range [2]struct {
			kind WireKind
			list []string
		}{{WireLifted, ww.cons.Lifted}, {WireCreatable, ww.cons.Creatable}} {
			if len(opt.list) == 0 {
				continue
			}
			if err := ww.marshal([]any{uint64(opt.kind), opt.list}); err != nil {
				return err
			}
		}
	}
	return ww.marshal(rec)
}

// list renders a declaration, an absent one as the empty list it means.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
	dec     *cbor.Decoder
	rec     WireRecord
	err     error
	cons    WireConstraints
	pending []cbor.RawMessage // the payload record that ended the constraint block
	headed  bool
}

// NewWireReader reads a contribution stream from r.
func NewWireReader(r io.Reader) *WireReader {
	return &WireReader{dec: cbor.NewDecoder(r)}
}

// Constraints are what the stream declares, read from its leading records alone — so a
// caller inspects them, and refuses what it will not grant, before taking the payload.
func (wr *WireReader) Constraints() (WireConstraints, error) {
	if err := wr.readConstraints(); err != nil {
		return WireConstraints{}, err
	}
	return wr.cons, nil
}

// readConstraints consumes the leading constraint records, once. The first payload
// record closes the block and waits for Next.
func (wr *WireReader) readConstraints() error {
	if wr.headed {
		return wr.err
	}
	wr.headed = true
	seen := map[WireKind]bool{}
	for {
		raw, ok, err := wr.decode()
		if err != nil {
			wr.err = err
			return err
		}
		if !ok {
			break
		}
		kind, err := wireKind(raw)
		if err != nil {
			wr.err = err
			return err
		}
		if !constraintKind(kind) {
			wr.pending = raw
			break
		}
		if err := wr.readConstraint(kind, raw, seen); err != nil {
			wr.err = err
			return err
		}
	}
	// An empty stream declares nothing and admits nothing, so it needs no records.
	if len(seen) == 0 && wr.pending == nil {
		return nil
	}
	if !seen[WireBranches] || !seen[WireReferencable] {
		wr.err = ErrWireNoHeader
	}
	return wr.err
}

// readConstraint records one declaration, narrowing against any earlier one of its
// kind so repeats compose rather than replace.
func (wr *WireReader) readConstraint(kind WireKind, raw []cbor.RawMessage, seen map[WireKind]bool) error {
	if len(raw) < 2 {
		return WithDetail(ErrWire, "constraint record carries no list")
	}
	var list []string
	if err := cbor.Unmarshal(raw[1], &list); err != nil {
		return Wrap(ErrWire, err)
	}
	slot, merge := wr.slot(kind)
	if seen[kind] {
		*slot = merge(*slot, list)
		return nil
	}
	seen[kind] = true
	*slot = list
	return nil
}

// slot returns where a kind's declaration is held, and how a repeat of it merges.
func (wr *WireReader) slot(kind WireKind) (*[]string, func(a, b []string) []string) {
	switch kind {
	case WireBranches:
		return &wr.cons.Branches, intersect
	case WireReferencable:
		return &wr.cons.Referencable, narrowScopes
	case WireCreatable:
		return &wr.cons.Creatable, intersect
	default:
		return &wr.cons.Lifted, intersect
	}
}

// Next decodes the next record, returning false at end of stream or on error.
// The kinds differ in arity, so the elements are taken raw and read per kind.
func (wr *WireReader) Next() bool {
	if wr.readConstraints() != nil {
		return false
	}
	raw := wr.pending
	wr.pending = nil
	if raw == nil {
		var ok bool
		var err error
		raw, ok, err = wr.decode()
		switch {
		case err != nil:
			return wr.fail(err)
		case !ok:
			return false
		}
	}
	kind, err := wireKind(raw)
	if err != nil {
		return wr.fail(err)
	}
	// The block is closed, so a declaration arriving here would bind records already
	// applied.
	if constraintKind(kind) {
		return wr.fail(ErrWireLateConstraint)
	}
	// The kind decides before the shape does, so an unrecognised record is reported as
	// one rather than as a payload of the wrong arity.
	if kind != WireClaim && kind != WireContent {
		return wr.fail(WithDetail(ErrWireKind, strconv.FormatUint(uint64(kind), 10)))
	}
	if len(raw) < 3 {
		return wr.fail(WithDetail(ErrWire, "record has fewer than three elements"))
	}
	key, payload, ok := wr.keyPayload(raw)
	if !ok {
		return false
	}
	if kind == WireClaim {
		return wr.readClaim(raw, key, payload)
	}
	return wr.readContent(key, payload)
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
	if !slices.Contains(wr.cons.Branches, branch) {
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
