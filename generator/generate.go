// package: generator / testkit
// type:    tool
// job:     Generate — build a deterministic kitchen-sink Ranke-Archive from a Spec, driving the dev Sequencer's write path; return a Manifest naming every corner
// limits:  builds via the public ranke API + the dev testkit adapters; the caller supplies the Universe (-> adapter); asserts nothing (-> tests).
package generator

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flocko-motion/ranke-go"
	devhist "github.com/flocko-motion/ranke-go/adapter/history/dev"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
)

// Manifest is the map of a generated archive: the head to open it at, plus the
// ids of the notable claims so a test can target a specific corner without
// re-deriving anything.
type Manifest struct {
	Spec Spec

	Head         ranke.Id   // archive head k′ — open with ranke.NewArchive(ctx, u, Head)
	Contributors []ranke.Id // the signing contributors (index 0 is the operator)
	Sources      []ranke.Id
	Derivations  []ranke.Id
	Entities     []ranke.Id
	Relations    []ranke.Id

	DiffChainHead ranke.Id   // head of the longest contribution/diff chain
	ExternalBlobs []ranke.Id // source claims whose content is external (hash-addressed)
	HighDegree    ranke.Id   // the derivation citing the most sources
	Expiries      []ranke.Id // contribution/expiry claims (key expiry)
	Deletes       []ranke.Id // contribution/delete tombstones

	ClaimCount int // claims contributed (excludes Sequencer-minted heads/tables)
}

// Generate builds the archive described by spec into u, returning its
// Manifest. It stands up the dev testkit (a deterministic clock, an in-memory
// history, the blocking dev Sequencer) over u, assembles one contribution
// carrying every feasible corner, and merges it — so u ends holding a real
// archive reachable from Manifest.Head. Deterministic: same (u-kind, spec) →
// identical ids on every run.
func Generate(ctx context.Context, u ranke.Universe, spec Spec) (*Manifest, error) {
	clock := NewClock(spec.Base, spec.Step)
	hist := devhist.New(clock)

	// Contributor 0 is the operator the Sequencer signs branch tables with.
	op, err := contributor(ctx, spec.Seed, 0, clock.Tick())
	if err != nil {
		return nil, fmt.Errorf("generator: operator: %w", err)
	}
	seq, err := devseq.NewSequencer(ctx, u, hist, op, clock)
	if err != nil {
		return nil, fmt.Errorf("generator: sequencer: %w", err)
	}

	b := &builder{ctx: ctx, u: u, spec: spec, clock: clock}
	b.contributors(op)
	b.sources()
	b.diffChain()
	b.derivations()
	b.entities()
	b.relations()
	b.expiries()
	b.deletes()
	if b.err != nil {
		return nil, b.err
	}

	head, err := seq.AddClaims(ctx, b.batch)
	if err != nil {
		return nil, fmt.Errorf("generator: merge: %w", err)
	}

	m := b.manifest
	m.Spec = spec
	m.Head = head
	m.ClaimCount = len(b.batch)
	return &m, nil
}

// builder accumulates the contribution batch, threading the shared clock so
// every claim's created_at is monotone (references are always built — and
// stamped — before the claims that cite them). The first error stops the run.
type builder struct {
	ctx   context.Context
	u     ranke.Universe
	spec  Spec
	clock *Clock

	contribs  []ranke.Contributor
	srcClaims []ranke.Claim // sources available as derivation inputs
	batch     []ranke.Claim
	manifest  Manifest
	err       error
}

// fail records the first error; subsequent build steps become no-ops.
func (b *builder) fail(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// who round-robins claims across the contributor set, so the archive is
// multi-authored (a signing "chain" of independent keys).
func (b *builder) who(i int) ranke.Contributor {
	return b.contribs[i%len(b.contribs)]
}

// add appends a claim to the batch and returns it (nil-safe under error).
func (b *builder) add(c ranke.Claim, err error) ranke.Claim {
	if b.err != nil {
		return nil
	}
	if err != nil {
		b.fail(err)
		return nil
	}
	b.batch = append(b.batch, c)
	return c
}

// contributors builds the operator plus spec.Contributors-1 more signing
// contributors (each an initial node carrying its key). The operator is
// already stored by the Sequencer, so only the others join the batch.
func (b *builder) contributors(op ranke.Contributor) {
	b.contribs = []ranke.Contributor{op}
	b.manifest.Contributors = []ranke.Id{op.ID()}
	for i := 1; i < b.spec.Contributors; i++ {
		c, err := contributor(b.ctx, b.spec.Seed, i, b.clock.Tick())
		if err != nil {
			b.fail(fmt.Errorf("contributor %d: %w", i, err))
			return
		}
		b.contribs = append(b.contribs, c)
		b.manifest.Contributors = append(b.manifest.Contributors, c.ID())
		b.add(c, nil)
	}
}

// sources builds spec.Sources source/* claims: the first ExternalBlobs carry
// external (hash-addressed) content, then tiny and large inline blobs
// alternate. One source carries an oversized field value.
func (b *builder) sources() {
	for i := 0; i < b.spec.Sources && b.err == nil; i++ {
		who := b.who(i)
		var c ranke.Claim
		switch {
		case i < b.spec.ExternalBlobs:
			c = b.externalSource(who, i)
		case i%2 == 0:
			c = b.inlineSource(who, i, b.spec.TinyBlobBytes, i == 0)
		default:
			c = b.inlineSource(who, i, b.spec.LargeBlobBytes, false)
		}
		if c != nil {
			b.srcClaims = append(b.srcClaims, c)
			b.manifest.Sources = append(b.manifest.Sources, c.ID())
		}
	}
}

// inlineSource builds a source/* claim with n bytes of inline content; when
// oversized, it also carries an oversized field value (that corner).
func (b *builder) inlineSource(who ranke.Contributor, i, n int, oversized bool) ranke.Claim {
	cb := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent(fill(b.spec.Seed, "src", i, n)).
		WithCreatedAt(b.clock.Tick())
	if oversized && b.spec.OversizedFieldBytes > 0 {
		big := fill(b.spec.Seed, "field", i, b.spec.OversizedFieldBytes)
		cb = cb.WithField("note", string(big))
	}
	return b.add(cb.Sign())
}

// externalSource builds a source/* claim whose content lives in the Universe
// under its hash, putting the bytes there too.
func (b *builder) externalSource(who ranke.Contributor, i int) ranke.Claim {
	blob := fill(b.spec.Seed, "ext", i, b.spec.LargeBlobBytes)
	hash, err := ranke.HashContent(blob)
	if err != nil {
		b.fail(fmt.Errorf("external hash %d: %w", i, err))
		return nil
	}
	if err := b.u.PutContents(b.ctx, []ranke.ContentBlob{{Hash: hash, Content: blob}}); err != nil {
		b.fail(fmt.Errorf("put external content %d: %w", i, err))
		return nil
	}
	c := b.add(ranke.NewClaim(ranke.TypeSource("blob"), who).
		WithExternalContent(hash, uint64(len(blob))).
		WithCreatedAt(b.clock.Tick()).
		Sign())
	if c != nil {
		b.manifest.ExternalBlobs = append(b.manifest.ExternalBlobs, c.ID())
	}
	return c
}

// diffChain builds one base source plus DiffChainLen revisions, each a
// contribution/diff overlay over the previous — the long-diff-chain corner.
func (b *builder) diffChain() {
	if b.err != nil {
		return
	}
	base := b.add(ranke.NewClaim(ranke.TypeSource("note"), b.who(0)).
		WithInlineContent(fill(b.spec.Seed, "diff", 0, b.spec.TinyBlobBytes)).
		WithCreatedAt(b.clock.Tick()).
		Sign())
	prev := base
	for r := 1; r <= b.spec.DiffChainLen && prev != nil && b.err == nil; r++ {
		prev = b.add(ranke.NewClaim(ranke.TypeSource("note"), b.who(r)).
			WithDiff(prev.ID()).
			WithField("rev", strconv.Itoa(r)).
			WithCreatedAt(b.clock.Tick()).
			Sign())
	}
	if prev != nil {
		b.manifest.DiffChainHead = prev.ID()
	}
}

// derivations builds spec.Derivations derivation/* claims citing sources. The
// first is high-degree — it cites up to MaxEdgeDegree sources at once; the
// rest cite a single source (the low-degree norm).
func (b *builder) derivations() {
	for i := 0; i < b.spec.Derivations && len(b.srcClaims) > 0 && b.err == nil; i++ {
		degree := 1
		if i == 0 {
			degree = min(b.spec.MaxEdgeDegree, len(b.srcClaims))
		}
		edges := make([]ranke.Edge, 0, degree)
		for d := 0; d < degree; d++ {
			src := b.srcClaims[(i+d)%len(b.srcClaims)]
			e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
			if err != nil {
				b.fail(fmt.Errorf("derivation edge %d/%d: %w", i, d, err))
				return
			}
			edges = append(edges, e)
		}
		c := b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(i)).
			WithInlineContent(fill(b.spec.Seed, "deriv", i, b.spec.TinyBlobBytes)).
			WithEdges(edges...).
			WithCreatedAt(b.clock.Tick()).
			Sign())
		if c != nil {
			b.manifest.Derivations = append(b.manifest.Derivations, c.ID())
			if i == 0 {
				b.manifest.HighDegree = c.ID()
			}
		}
	}
}

// entities builds spec.Entities entity/* claims, each with a derivation edge
// to a source (the §3.5 provenance a semantic claim requires).
func (b *builder) entities() {
	for i := 0; i < b.spec.Entities && len(b.srcClaims) > 0 && b.err == nil; i++ {
		src := b.srcClaims[i%len(b.srcClaims)]
		de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
		if err != nil {
			b.fail(fmt.Errorf("entity provenance edge %d: %w", i, err))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeEntity("person"), b.who(i)).
			WithInlineContent(fill(b.spec.Seed, "ent", i, b.spec.TinyBlobBytes)).
			WithEdges(de).
			WithCreatedAt(b.clock.Tick()).
			Sign())
		if c != nil {
			b.manifest.Entities = append(b.manifest.Entities, c.ID())
		}
	}
}

// relations builds spec.Relations relation/* claims, each a reified assertion
// between two entities: a from-edge to one and a to-edge to the other (both
// relation_direction polarities), plus the derivation edge for provenance.
func (b *builder) relations() {
	ents := b.manifest.Entities
	if len(ents) < 2 || len(b.srcClaims) == 0 {
		return
	}
	for i := 0; i < b.spec.Relations && b.err == nil; i++ {
		from := ents[(2*i)%len(ents)]
		to := ents[(2*i+1)%len(ents)]
		src := b.srcClaims[i%len(b.srcClaims)]
		de, e1 := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
		fromE, e2 := ranke.NewEdge(ranke.EdgeConfig{Reference: from, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationFrom})
		toE, e3 := ranke.NewEdge(ranke.EdgeConfig{Reference: to, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationTo})
		if e1 != nil || e2 != nil || e3 != nil {
			b.fail(fmt.Errorf("relation edges %d: %v/%v/%v", i, e1, e2, e3))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeRelation("knows"), b.who(i)).
			WithEdges(de, fromE, toE).
			WithCreatedAt(b.clock.Tick()).
			Sign())
		if c != nil {
			b.manifest.Relations = append(b.manifest.Relations, c.ID())
		}
	}
}

// expiries mints spec.KeyExpiries contribution/expiry claims. Each is a claim
// (attributed to the operator) carrying a contribution/expiry edge to a
// contributor and a pubkey_expires_after field naming the last time that key
// is valid (paper §Types). Everything is just a claim — there is no special
// API, the type is the open-vocabulary string "contribution/expiry".
func (b *builder) expiries() {
	if len(b.contribs) < 2 {
		return // no non-operator contributor to expire
	}
	// A fixed future instant, deterministic like everything else.
	expiresAt := b.spec.Base.Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	for i := 0; i < b.spec.KeyExpiries && b.err == nil; i++ {
		// Never expire the operator's key — it signs the branch tables.
		target := b.contribs[1+(i%(len(b.contribs)-1))]
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: "contribution/expiry"})
		if err != nil {
			b.fail(fmt.Errorf("expiry edge %d: %w", i, err))
			return
		}
		c := b.add(ranke.NewClaim("contribution/expiry", b.contribs[0]).
			WithEdges(e).
			WithField("pubkey_expires_after", expiresAt).
			WithCreatedAt(b.clock.Tick()).
			Sign())
		if c != nil {
			b.manifest.Expiries = append(b.manifest.Expiries, c.ID())
		}
	}
}

// deletes mints spec.Deletes contribution/delete tombstones. Each is a claim
// (attributed to the operator) whose contribution/delete edge names a claim
// whose bytes were removed, documenting the gap (paper §Types). We emit the
// tombstone structurally; the bytes are not actually purged (a purge is a
// separate negative-test corner). The paper defines contribution/delete as an
// edge subtype; the tombstone's node type is our open-vocabulary choice.
func (b *builder) deletes() {
	for i := 0; i < b.spec.Deletes && len(b.srcClaims) > 0 && b.err == nil; i++ {
		target := b.srcClaims[i%len(b.srcClaims)]
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: "contribution/delete"})
		if err != nil {
			b.fail(fmt.Errorf("delete edge %d: %w", i, err))
			return
		}
		c := b.add(ranke.NewClaim("contribution/delete", b.contribs[0]).
			WithEdges(e).
			WithCreatedAt(b.clock.Tick()).
			Sign())
		if c != nil {
			b.manifest.Deletes = append(b.manifest.Deletes, c.ID())
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
