// package: tests/generator / testkit
// type:    tool
// job:     Generate — build a deterministic kitchen-sink Ranke-Archive from a Spec, driving the dev Sequencer's write path; return a Manifest naming every corner
// limits:  builds via the public ranke API + the dev testkit adapters; the caller supplies the Universe (-> adapter); asserts nothing (-> tests).
package generator

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/flocko-motion/ranke-go"
	devhist "github.com/flocko-motion/ranke-go/adapter/history/dev"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
)

var errGenerate = errors.New("generator")

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
	TinyBlob      ranke.Id   // the source whose content is TinyBlobBytes long
	HighDegree    ranke.Id   // the derivation citing the most sources
	Expiries      []ranke.Id // contribution/expiry claims (key expiry)
	Deletes       []ranke.Id // contribution/delete tombstones

	ClaimCount int      // claims contributed (excludes Sequencer-minted heads/tables)
	Revisions  int      // contributions merged = branch-table revisions
	Branches   []string // branch names the archive carries
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
		return nil, fmt.Errorf("%w: operator: %w", errGenerate, err)
	}
	seq, err := devseq.NewSequencer(ctx, u, hist, op, clock)
	if err != nil {
		return nil, fmt.Errorf("%w: sequencer: %w", errGenerate, err)
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

	// A real archive grows over many contributions, not one: split the batch
	// into contributions of varying size (small common, large rare)
	branches := branchNames(len(b.batch), spec.Seed, spec.Branches)
	var head ranke.Id
	revisions := 0
	for i, chunk := range contributionChunks(b.batch, spec.Seed, len(branches)) {
		// Round-robin contributions across the branches (main takes the first,
		// so the shared base lands there);
		head, err = seq.AddClaimsToBranch(ctx, branches[i%len(branches)], chunk)
		if err != nil {
			return nil, fmt.Errorf("%w: merge: %w", errGenerate, err)
		}
		revisions++
	}

	m := b.manifest
	m.Spec = spec
	m.Head = head
	m.ClaimCount = len(b.batch)
	m.Revisions = revisions
	m.Branches = branches
	return &m, nil
}

// contributionChunks splits an already-dependency-ordered batch into
// contributions whose sizes follow a skewed distribution — small common, large
// rare (exponential) — so the archive is built over many revisions like a real
// one, not a single outlier contribution. The mean size grows with the batch so
// the contribution COUNT stays bounded: the dev Sequencer re-verifies each
// contribution's closure, so an unbounded count would be quadratic. Deterministic
// per seed (chunking only shapes the Sequencer-minted revision chain; contributed
// claim ids do not depend on it).
func contributionChunks(batch []ranke.Claim, seed int64, minChunks int) [][]ranke.Claim {
	n := len(batch)
	if n == 0 {
		return nil
	}
	// Each branch takes a contribution in turn, so reaching them all needs at
	// least that many. A small batch splits evenly to get there.
	if minChunks > 1 && n >= minChunks {
		size := (n + minChunks - 1) / minChunks
		var chunks [][]ranke.Claim
		for i := 0; i < n; i += size {
			end := min(i+size, n)
			chunks = append(chunks, batch[i:end])
		}
		return chunks
	}
	rng := rand.New(rand.NewSource(seed))
	mean := 4.0
	if m := float64(n) / 150; m > mean {
		mean = m // cap the count near ~150 for large graphs
	}
	var chunks [][]ranke.Claim
	for i := 0; i < n; {
		size := 1 + int(rng.ExpFloat64()*mean)
		if i+size > n {
			size = n - i
		}
		chunks = append(chunks, batch[i:i+size])
		i += size
	}
	return chunks
}

// builder accumulates the contribution batch, threading the shared clock so
// every claim's created_at is monotone (references are always built — and
// stamped — before the claims that cite them). The first error stops the run.
type builder struct {
	ctx   context.Context
	u     ranke.Universe
	spec  Spec
	clock *Clock

	contribs      []ranke.Contributor
	srcClaims     []ranke.Claim // sources available as derivation inputs
	entClaims     []ranke.Claim // entity claims, kept as objects so relations can read their heights
	batch         []ranke.Claim
	manifest      Manifest
	oversizedDone bool // the oversized-field corner is applied once
	tinyDone      bool // likewise the tiny-content corner
	err           error
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
			b.fail(fmt.Errorf("%w: contributor %d: %w", errGenerate, i, err))
			return
		}
		b.contribs = append(b.contribs, c)
		b.manifest.Contributors = append(b.manifest.Contributors, c.ID())
		b.add(c, nil)
	}
}

// sources builds spec.Sources source/* claims: the first ExternalBlobs carry
// external (hash-addressed) content, then inline notes and further external
// blobs alternate. One source carries an oversized field value.
func (b *builder) sources() {
	for i := 0; i < b.spec.Sources && b.err == nil; i++ {
		who := b.who(i)
		var c ranke.Claim
		switch {
		case i < b.spec.ExternalBlobs:
			c = b.externalSource(who, i)
		case i%2 == 0:
			c = b.inlineSource(who, i)
		default:
			// Large data goes in EXTERNAL content (content-addressed), never
			// inline: inline bytes are bundled in the claim record and cannot be
			// served from a lower tier, so a caching backend (neo4j) can't
			// round-trip them. "Put large data in content" — the ADT convention.
			c = b.externalSource(who, i)
		}
		if c != nil {
			b.srcClaims = append(b.srcClaims, c)
			b.manifest.Sources = append(b.manifest.Sources, c.ID())
		}
	}
}

// inlineSource builds a source/note claim with legible inline text. The first
// inline source also carries the oversized field value (that corner, applied
// once) — a large-but-valid field, kept under the ADT's field cap.
func (b *builder) inlineSource(who ranke.Contributor, i int) ranke.Claim {
	text := textFor(b.spec.Seed, "src", i)
	tiny := !b.tinyDone && b.spec.TinyBlobBytes > 0
	if tiny {
		text = tinyFor(b.spec.Seed, "src", i, b.spec.TinyBlobBytes)
		b.tinyDone = true
	}
	cb := ranke.NewClaim(ranke.TypeSource("note"), who).
		WithInlineContent([]byte(text)).
		WithEncoding(ranke.EncodingPlain). // a note is legible text
		WithCreatedAt(b.clock.Tick())
	if !b.oversizedDone && b.spec.OversizedFieldBytes > 0 {
		// A field value is text, not bytes — hex-encode so it is valid UTF-8
		// (a caching backend stores field values as string properties; raw
		// binary would not round-trip). Content, by contrast, may be binary.
		big := hex.EncodeToString(fill(b.spec.Seed, "field", i, b.spec.OversizedFieldBytes/2))
		cb = cb.WithField("note", big)
		b.oversizedDone = true
	}
	c := b.add(cb.WithHeight(ranke.HeightOf(who)).Sign())
	if tiny && c != nil {
		b.manifest.TinyBlob = c.ID()
	}
	return c
}

// externalSource builds a source/* claim whose content lives in the Universe
// under its hash, putting the bytes there too.
func (b *builder) externalSource(who ranke.Contributor, i int) ranke.Claim {
	blob := fill(b.spec.Seed, "ext", i, b.spec.LargeBlobBytes)
	hash, err := ranke.HashContent(blob)
	if err != nil {
		b.fail(fmt.Errorf("%w: external hash %d: %w", errGenerate, i, err))
		return nil
	}
	if err := b.u.PutContents(b.ctx, []ranke.ContentBlob{{Hash: hash, Content: blob}}); err != nil {
		b.fail(fmt.Errorf("%w: put external content %d: %w", errGenerate, i, err))
		return nil
	}
	c := b.add(ranke.NewClaim(ranke.TypeSource("blob"), who).
		WithExternalContent(hash, uint64(len(blob))).
		WithEncoding(ranke.EncodingOctetStream). // deterministic binary blob
		WithCreatedAt(b.clock.Tick()).
		WithHeight(ranke.HeightOf(who)).
		Sign())
	if c != nil {
		b.manifest.ExternalBlobs = append(b.manifest.ExternalBlobs, c.ID())
	}
	return c
}

// diffChain builds one base derivation plus DiffChainLen revisions, each a
// contribution/diff overlay over the previous — the long-diff-chain corner.
// Revision belongs on a derivation (a distillation refined over successive
// contributions), never a source: sources are root artifacts that reference
// nothing but their contributor, so they are never diffed over one another.
func (b *builder) diffChain() {
	if b.err != nil || len(b.srcClaims) == 0 {
		return
	}
	src := b.srcClaims[0] // the chain distils this source
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
	if err != nil {
		b.fail(fmt.Errorf("%w: diff-chain provenance edge: %w", errGenerate, err))
		return
	}
	base := b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(0)).
		WithInlineContent([]byte(textFor(b.spec.Seed, "diff", 0))).
		WithEncoding(ranke.EncodingPlain). // a summary is legible text
		WithEdges(de).
		WithCreatedAt(b.clock.Tick()).
		WithHeight(ranke.HeightOf(b.who(0), src)).
		Sign())
	prev := base
	for r := 1; r <= b.spec.DiffChainLen && prev != nil && b.err == nil; r++ {
		// Each revision re-affirms its provenance (the §3.5 derivation/source
		// edge, checked per delta) as a NAMED edge — diff claims name their
		// edges, and the same name keeps it one edge through the chain.
		re, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: src.ID(),
			Type:      ranke.TypeDerivation("source"),
			Fields:    map[string]string{ranke.FieldName: "source"},
		})
		if err != nil {
			b.fail(fmt.Errorf("%w: diff-chain revision edge %d: %w", errGenerate, r, err))
			return
		}
		prev = b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(r)).
			WithDiff(prev.ID()).
			WithEdges(re).
			WithField("rev", strconv.Itoa(r)).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(r), prev, src)).
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
		refs := []ranke.Claim{b.who(i)}
		for d := 0; d < degree; d++ {
			src := b.srcClaims[(i+d)%len(b.srcClaims)]
			e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
			if err != nil {
				b.fail(fmt.Errorf("%w: derivation edge %d/%d: %w", errGenerate, i, d, err))
				return
			}
			edges = append(edges, e)
			refs = append(refs, src)
		}
		c := b.add(ranke.NewClaim(ranke.TypeDerivation("summary"), b.who(i)).
			WithInlineContent([]byte(textFor(b.spec.Seed, "deriv", i))).
			WithEncoding(ranke.EncodingPlain). // a summary is legible text
			WithEdges(edges...).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(refs...)).
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
			b.fail(fmt.Errorf("%w: entity provenance edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeEntity("person"), b.who(i)).
			WithInlineContent([]byte(nameFor(b.spec.Seed, i))).
			WithEncoding(ranke.EncodingPlain). // a person entity's content is a name
			WithEdges(de).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(i), src)).
			Sign())
		if c != nil {
			b.manifest.Entities = append(b.manifest.Entities, c.ID())
			b.entClaims = append(b.entClaims, c)
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
		fromClaim := b.entClaims[(2*i)%len(b.entClaims)]
		toClaim := b.entClaims[(2*i+1)%len(b.entClaims)]
		from := ents[(2*i)%len(ents)]
		to := ents[(2*i+1)%len(ents)]
		src := b.srcClaims[i%len(b.srcClaims)]
		de, e1 := ranke.NewEdge(ranke.EdgeConfig{Reference: src.ID(), Type: ranke.TypeDerivation("source")})
		fromE, e2 := ranke.NewEdge(ranke.EdgeConfig{Reference: from, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationFrom})
		toE, e3 := ranke.NewEdge(ranke.EdgeConfig{Reference: to, Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationTo})
		if e1 != nil || e2 != nil || e3 != nil {
			b.fail(fmt.Errorf("%w: relation edges %d: %v/%v/%v", errGenerate, i, e1, e2, e3))
			return
		}
		c := b.add(ranke.NewClaim(ranke.TypeRelation("knows"), b.who(i)).
			WithEdges(de, fromE, toE).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.who(i), src, fromClaim, toClaim)).
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
			b.fail(fmt.Errorf("%w: expiry edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim("contribution/expiry", b.contribs[0]).
			WithEdges(e).
			WithField("pubkey_expires_after", expiresAt).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.contribs[0], target)).
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
			b.fail(fmt.Errorf("%w: delete edge %d: %w", errGenerate, i, err))
			return
		}
		c := b.add(ranke.NewClaim("contribution/delete", b.contribs[0]).
			WithEdges(e).
			WithCreatedAt(b.clock.Tick()).
			WithHeight(ranke.HeightOf(b.contribs[0], target)).
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
