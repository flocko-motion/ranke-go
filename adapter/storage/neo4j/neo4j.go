// package: neo4j / persistence-cache
// type:    adapter
// job:     a graph-native CACHE Universe on neo4j — stores claim structure (nodes, edges) so closure/membership run as native Cypher instead of edge walks
// limits:  pure cache — no canonical CBOR, no external content, inline content only up to a cap (default 4 KiB); stack over a durable Universe (-> adapter/s3, adapter/fs) that holds the bytes and serves content misses
//
// Package neo4j is the first pure-caching ranke Universe. It deconstructs each
// claim into neo4j's native typed graph — a node labelled with the claim's type
// (e.g. `source/email`) and its edges as relationships typed by the edge type
// (e.g. `derivation/source`) — so closure and membership run as native Cypher,
// and the graph is legible in the neo4j Browser. (Requires Neo4j ≥ 5.26 for the
// dynamic label/type projection.)
//
// It deliberately does NOT store the canonical CBOR, external content, or
// inline content beyond WithContentCap (default 4 KiB). Inline content of a
// text encoding rides along as a legible `content` property on the node (or
// relationship); binary, over-cap, and external content are left to the durable
// Universe this cache is stacked over — nothing is stored outside a claim's own
// node. A claim's id depends only on content_hash + content_size — never the
// content bytes — so claims reconstruct id-faithfully via ranke.AssembleClaim,
// with content this cache lacks reconstructing as external.
//
// New takes an already-configured neo4j driver so the adapter stays free of
// connection/credential concerns: production wires a real driver, tests point
// one at a container.
//
// Each claim's committed height (§4.1) is stored as a node property — an
// engrained property like content_hash — and served natively by
// GetClaimHeights, the field that later unlocks cheap branch-scoped closures
// (a membership cache keyed by height). Branch-membership metadata itself is
// the next step; today closure is a variable-length path over :REFERENCES.
package neo4j

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/flocko-motion/ranke-go"
)

// defaultContentCap is the largest inline content the cache stores inline.
const defaultContentCap = 4 << 10 // 4 KiB

// New returns a graph-native cache Universe over an already-configured driver.
// Connection, auth, and (unless WithDatabase is given) the target database
// live on the driver.
func New(driver neo4jdriver.DriverWithContext, opts ...Option) ranke.Universe {
	u := &neo4jUniverse{driver: driver, contentCap: defaultContentCap, tier: ranke.StorageTierEager}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Option configures the neo4j cache Universe.
type Option func(*neo4jUniverse)

// WithContentCap sets the largest inline content (bytes) the cache keeps
// inline. Larger content — and all external content — is not held here; it is
// served by the durable Universe this cache is stacked over. Default 4 KiB.
// A cap of 0 stores no content at all (structure-only).
func WithContentCap(n int) Option {
	return func(u *neo4jUniverse) {
		if n >= 0 {
			u.contentCap = n
		}
	}
}

// WithDatabase selects the neo4j database (multi-database deployments).
// Default is the driver's configured default database.
func WithDatabase(name string) Option {
	return func(u *neo4jUniverse) { u.database = name }
}

// WithTier sets the write role the cache serves in a stack (Capabilities.Tier).
// Default is eager — the write-through queryable layer, written synchronously
// (best-effort) alongside the source of truth so reads and RQL hit an
// up-to-date graph. neo4j holds a lossy projection (no verbatim CBOR), so it
// can never be authoritative; a deployment may still choose background or lazy.
func WithTier(t ranke.StorageTier) Option {
	return func(u *neo4jUniverse) { u.tier = t }
}

type neo4jUniverse struct {
	driver     neo4jdriver.DriverWithContext
	database   string
	contentCap int
	tier       ranke.StorageTier
}

// Compile-time proof the adapter satisfies the full Universe contract.
var _ ranke.Universe = (*neo4jUniverse)(nil)

var (
	errNilClaim = errors.New("adapter/neo4j: nil claim or id")
	errNilID    = errors.New("adapter/neo4j: nil id")
	errNilHash  = errors.New("adapter/neo4j: nil content hash")
	errQuery    = errors.New("adapter/neo4j: query")
)

// query runs a Cypher statement in an auto-commit transaction, scoped to the
// configured database when set.
func (u *neo4jUniverse) query(ctx context.Context, cypher string, params map[string]any) (*neo4jdriver.EagerResult, error) {
	if u.database != "" {
		return neo4jdriver.ExecuteQuery(ctx, u.driver, cypher, params,
			neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase(u.database))
	}
	return neo4jdriver.ExecuteQuery(ctx, u.driver, cypher, params, neo4jdriver.EagerResultTransformer)
}

// cypherPutClaims projects claims into neo4j's native typed graph. Nodes are
// MERGE'd by id with no label (so a reference target and its later full claim
// are the same node) and given their type as a dynamic label; edges are
// relationships typed dynamically by the edge type. Inline content rides along
// as a legible `content` property; a claim's extension fields ride as their own
// properties (SET += fields), so the browser shows and can query them. Requires
// Neo4j ≥ 5.26 (dynamic labels/types via $()). A null property is simply unset.
const cypherPutClaims = `
UNWIND $claims AS c
MERGE (n {id: c.id})
SET n:$(c.type)
SET n += c.fields
SET n.encoding = c.encoding, n.created_at = c.created_at, n.height = c.height,
    n.content_hash = c.content_hash, n.content_size = c.content_size,
    n.content = c.content
WITH n, c
UNWIND c.edges AS e
MERGE (t {id: e.reference})
MERGE (n)-[r:$(e.type) {edge_id: e.edge_id}]->(t)
SET r += e.fields
SET r.encoding = e.encoding, r.direction = e.direction, r.content_hash = e.content_hash,
    r.content_size = e.content_size, r.content = e.content`

// PutClaims projects each claim into neo4j's native typed graph: a node
// labelled with the claim's type, its edges as relationships typed by the edge
// type, and any legible inline content as a `content` property on the node /
// relationship — nothing is stored outside a claim's own node. The canonical
// CBOR is not stored; external, binary, and over-cap content are left to the
// durable layer. Nodes MERGE by id (a reference target is a labelless stub
// until its own claim adds the type label), so re-puts are idempotent.
func (u *neo4jUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	if len(cs) == 0 {
		return nil
	}
	claims := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		claims = append(claims, u.claimParam(c))
	}
	if _, err := u.query(ctx, cypherPutClaims, map[string]any{"claims": claims}); err != nil {
		return fmt.Errorf("%w: put claims: %w", errQuery, err)
	}
	return nil
}

// cypherGetClaims fetches nodes by id (label-agnostic — the adapter reads by
// id; labels are the human/native-query projection) that are full claims
// (size(labels) > 0, i.e. not a bare reference stub), with each node's labels
// (its type) and its outgoing relationships (each carrying its own type via
// type(r)).
const cypherGetClaims = `
UNWIND $ids AS id
MATCH (n {id: id})
WHERE size(labels(n)) > 0
RETURN properties(n) AS node, labels(n) AS labels,
       [(n)-[r]->(t) | {props: properties(r), rtype: type(r), ref: t.id}] AS edges`

// GetClaims reconstructs claims from their graph nodes (+ edge relationships),
// re-inlining any legible content the cache holds (the `content` property) and
// materialising diff overlays like any Universe. A requested id absent from the
// cache is a miss (ranke.ErrNotFound) so the stack falls through to the durable
// layer.
func (u *neo4jUniverse) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	idStrs := make([]string, len(ids))
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		idStrs[i] = id.String()
	}

	res, err := u.query(ctx, cypherGetClaims, map[string]any{"ids": idStrs})
	if err != nil {
		return nil, fmt.Errorf("%w: get claims: %w", errQuery, err)
	}

	type rec struct {
		props  map[string]any
		labels []any
		edges  []any
	}
	byID := make(map[string]rec, len(res.Records))
	for _, r := range res.Records {
		props, _ := valOf(r, "node").(map[string]any)
		labels, _ := valOf(r, "labels").([]any)
		edges, _ := valOf(r, "edges").([]any)
		byID[asString(props["id"])] = rec{props, labels, edges}
	}

	for i, id := range ids {
		r, ok := byID[id.String()]
		if !ok {
			return nil, fmt.Errorf("claim %s: %w", id, ranke.ErrNotFound)
		}
		parts, err := partsFromNode(id, r.props, r.labels, r.edges)
		if err != nil {
			return nil, err
		}
		c, err := ranke.AssembleClaim(parts)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	// Materialise diff overlays via the ADT default, honouring read opts.
	return ranke.DefaultMaterialize(ctx, u, out, opts...)
}

const cypherHasClaims = `
UNWIND $ids AS id
OPTIONAL MATCH (n {id: id})
RETURN id AS id, (n IS NOT NULL AND size(labels(n)) > 0) AS has`

// HasClaims reports which ids are present as full claims (a bare reference-
// target stub does not count).
func (u *neo4jUniverse) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	idStrs := make([]string, len(ids))
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		idStrs[i] = id.String()
		pos[id.String()] = i
	}
	res, err := u.query(ctx, cypherHasClaims, map[string]any{"ids": idStrs})
	if err != nil {
		return nil, fmt.Errorf("%w: has claims: %w", errQuery, err)
	}
	for _, r := range res.Records {
		if i, ok := pos[asString(valOf(r, "id"))]; ok {
			out[i], _ = valOf(r, "has").(bool)
		}
	}
	return out, nil
}

const cypherGetClaimHeights = `
UNWIND $ids AS id
MATCH (n {id: id})
WHERE size(labels(n)) > 0
RETURN id AS id, n.height AS height`

// GetClaimHeights returns each claim's committed height (§4.1) natively from
// the stored node property — no reconstruction, the reason height is engrained
// like content_hash. A requested id absent from the cache is a miss
// (ranke.ErrNotFound) so a stack falls through to the durable layer.
func (u *neo4jUniverse) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	out := make([]uint64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	idStrs := make([]string, len(ids))
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		idStrs[i] = id.String()
	}
	res, err := u.query(ctx, cypherGetClaimHeights, map[string]any{"ids": idStrs})
	if err != nil {
		return nil, fmt.Errorf("%w: get claim heights: %w", errQuery, err)
	}
	seen := make(map[string]uint64, len(res.Records))
	for _, r := range res.Records {
		seen[asString(valOf(r, "id"))] = uint64(asInt(valOf(r, "height")))
	}
	for i, id := range ids {
		h, ok := seen[id.String()]
		if !ok {
			return nil, fmt.Errorf("claim %s: %w", id, ranke.ErrNotFound)
		}
		out[i] = h
	}
	return out, nil
}

// Query answers an RQL read. For now it delegates to the reference executor
// over this Universe's own reads (forward-only, uses/connections refused); a
// future native lowering to Cypher — and ReverseWalk support, since neo4j holds
// edges both directions — would override this.
func (u *neo4jUniverse) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return ranke.DefaultQuery(ctx, u, q, scope)
}

// GetClaimsRaw always misses: a structure/query cache stores no CBOR. The
// ErrNotFound lets a stack route the request to the authoritative byte layer.
func (u *neo4jUniverse) GetClaimsRaw(_ context.Context, ids []ranke.Id) ([][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("adapter/neo4j: stores no claim CBOR (structure-only cache): %w", ranke.ErrNotFound)
}

// GetContents always misses: this cache holds no external content — inline
// content lives on the claim node and is served with the claim (GetClaims), so
// the external-content API never has anything here. ErrNotFound routes the
// request to the durable layer.
func (u *neo4jUniverse) GetContents(_ context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("adapter/neo4j: holds no external content (inline only, on the claim node): %w", ranke.ErrNotFound)
}

// PutContents is a no-op: content lives on the claim node (set by PutClaims),
// and external content belongs to the durable layer — neo4j stores nothing
// outside a claim's own node.
func (u *neo4jUniverse) PutContents(_ context.Context, _ []ranke.ContentBlob) error {
	return nil
}

// HasContents reports false for everything: the cache holds no external content.
func (u *neo4jUniverse) HasContents(_ context.Context, hashes []ranke.Id) ([]bool, error) {
	return make([]bool, len(hashes)), nil
}

// StreamContent always misses: the cache holds no external content to stream.
func (u *neo4jUniverse) StreamContent(_ context.Context, hash ranke.Id, _ uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errNilHash
	}
	return nil, fmt.Errorf("content %s: %w", hash, ranke.ErrNotFound)
}

// CopyClaims uses the ADT default walker; a native batched MERGE could
// override later.
func (u *neo4jUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, u, src, ids, opts...)
}

// CopyContents uses the ADT default walker.
func (u *neo4jUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, u, src, refs, opts...)
}

// --- Tags (Capabilities.Tags): mutable per-claim overlay ---
//
// Tags are node properties whose key carries ranke.ReservedPrefix ("_"). No
// fixed claim property (id, type, height, …) starts with "_", so the prefix
// cleanly separates tags; the tagger's keys (_br, _b_<branch>) already carry
// it, so they are stored verbatim. GetClaims injects them back onto the claim
// (partsFromNode); GetClaimTags is the tags-only shortcut.

const cypherGetClaimTags = `
UNWIND $ids AS id
MATCH (n {id: id})
WHERE size(labels(n)) > 0
RETURN id AS id, properties(n) AS node`

// GetClaimTags returns each claim's tags positionally; a claim absent from the
// cache (or with none) yields a nil map.
func (u *neo4jUniverse) GetClaimTags(ctx context.Context, claims []ranke.Id) ([]map[string]string, error) {
	out := make([]map[string]string, len(claims))
	if len(claims) == 0 {
		return out, nil
	}
	idStrs := make([]string, len(claims))
	pos := make(map[string]int, len(claims))
	for i, id := range claims {
		if id == nil {
			return nil, errNilID
		}
		idStrs[i] = id.String()
		pos[id.String()] = i
	}
	res, err := u.query(ctx, cypherGetClaimTags, map[string]any{"ids": idStrs})
	if err != nil {
		return nil, fmt.Errorf("%w: get claim tags: %w", errQuery, err)
	}
	for _, r := range res.Records {
		if i, ok := pos[asString(valOf(r, "id"))]; ok {
			props, _ := valOf(r, "node").(map[string]any)
			out[i] = tagsFrom(props)
		}
	}
	return out, nil
}

const cypherSetClaimsTags = `
UNWIND $rows AS row
MATCH (c {id: row.id})
SET c += row.props`

// SetClaimsTags applies tags per claim (keyed by id string): for each claim it
// clears every existing tag whose key matches a clearTags glob, then applies
// the new key→value pairs. Both happen via SET c += props, where a null value
// removes a property (the clear) and a string sets it.
func (u *neo4jUniverse) SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	// Clearing needs the current tag keys to know which to null out.
	var current map[string]map[string]string
	if len(clearTags) > 0 {
		ids := make([]ranke.Id, 0, len(tags))
		for s := range tags {
			id, err := ranke.ParseId(s)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		got, err := u.GetClaimTags(ctx, ids)
		if err != nil {
			return err
		}
		current = make(map[string]map[string]string, len(ids))
		for i, id := range ids {
			current[id.String()] = got[i]
		}
	}
	rows := make([]map[string]any, 0, len(tags))
	for s, kv := range tags {
		props := map[string]any{}
		for k := range current[s] {
			for _, pat := range clearTags {
				if ok, _ := path.Match(pat, k); ok {
					props[k] = nil // null removes the property
					break
				}
			}
		}
		for k, v := range kv {
			props[k] = tagParam(v) // store integer-valued tags (_br, _b_*) as native ints
		}
		if len(props) == 0 {
			continue
		}
		rows = append(rows, map[string]any{"id": s, "props": props})
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := u.query(ctx, cypherSetClaimsTags, map[string]any{"rows": rows}); err != nil {
		return fmt.Errorf("%w: set claims tags: %w", errQuery, err)
	}
	return nil
}

// Capabilities: a graph DB can overwrite, delete, and enumerate, is durable,
// and holds branch tags.
func (u *neo4jUniverse) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{
		Overwrite:  true,
		Delete:     true,
		Enumerate:  true,
		Persistent: true,
		Tags:       true,
		Tier:       u.tier,
	}
}

// Sync projects id's closure from src into the graph (via CopyClaims).
// TODO: native stub-check to skip revisions already fully projected.
func (u *neo4jUniverse) Sync(ctx context.Context, src ranke.Universe, id ranke.Id) <-chan ranke.SyncResult {
	return ranke.DefaultSync(ctx, u, src, id)
}

// Close is a no-op: the caller owns the driver's lifecycle.
func (u *neo4jUniverse) Close() error { return nil }

// valOf reads a named column from a record (nil when absent).
func valOf(r *neo4jdriver.Record, key string) any {
	v, _ := r.Get(key)
	return v
}
