// package: neo4j / persistence-cache
// type:    adapter
// job:     a graph-native CACHE Universe on neo4j — stores claim structure (nodes, edges) so closure/membership run as native Cypher instead of edge walks
// limits:  pure cache — no canonical CBOR, no external content, inline content only up to a cap (default 4 KiB); stack over a durable Universe (-> adapter/s3, adapter/fs) that holds the bytes and serves content misses
// dev:     a real neo4j runs IN your container — `services/neo4j.sh native up` (bolt://127.0.0.1:7687, user neo4j / pass rankeperfpass); the adapter tests auto-detect a reachable instance (no env var needed), else skip
package neo4j

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/flocko-motion/ranke-go"
)

// defaultContentCap is the largest inline content the cache stores inline.
const defaultContentCap = 4 << 10 // 4 KiB

// New returns a graph-native cache Universe over an already-configured driver,
// which carries connection, auth, and the target database (see WithDatabase).
func New(driver neo4jdriver.DriverWithContext, opts ...Option) ranke.Universe {
	u := &neo4jUniverse{driver: driver, contentCap: defaultContentCap, tier: ranke.StorageTierEager}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Option configures the neo4j cache Universe.
type Option func(*neo4jUniverse)

// WithContentCap sets the largest inline content (bytes) the cache keeps, the
// durable Universe below serving the rest. Default 4 KiB, 0 for structure-only.
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
// Default eager: written synchronously alongside the source of truth, so reads
// and RQL hit an up-to-date graph. A deployment may pick background or lazy.
func WithTier(t ranke.StorageTier) Option {
	return func(u *neo4jUniverse) { u.tier = t }
}

type neo4jUniverse struct {
	driver     neo4jdriver.DriverWithContext
	database   string
	contentCap int
	tier       ranke.StorageTier
	queries    atomic.Int64 // count of Cypher round-trips (tests assert one per RQL)
}

// Compile-time proof the adapter satisfies the full Universe contract.
var _ ranke.Universe = (*neo4jUniverse)(nil)

var (
	errNilClaim  = errors.New("adapter/neo4j: nil claim or id")
	errNilID     = errors.New("adapter/neo4j: nil id")
	errNilHash   = errors.New("adapter/neo4j: nil content hash")
	errQuery     = errors.New("adapter/neo4j: query")
	errDeltaForm = ranke.WithDetail(ranke.ErrUnsupported,
		"adapter/neo4j: delta form — this cache stores materialised claims only")
)

// query runs a Cypher statement in an auto-commit transaction, scoped to the
// configured database when set.
func (u *neo4jUniverse) query(ctx context.Context, cypher string, params map[string]any) (*neo4jdriver.EagerResult, error) {
	u.queries.Add(1)
	if u.database != "" {
		return neo4jdriver.ExecuteQuery(ctx, u.driver, cypher, params,
			neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase(u.database))
	}
	return neo4jdriver.ExecuteQuery(ctx, u.driver, cypher, params, neo4jdriver.EagerResultTransformer)
}

// cypherPutClaims projects claims into neo4j's native typed graph: MERGE by id so
// a reference target and its later full claim are one node, the claim type as a
// dynamic label, extension fields and inline content as properties. Dynamic
// labels via $() require Neo4j ≥ 5.26; a null property is unset.
const cypherPutNodes = `
UNWIND $claims AS c
MERGE (n {id: c.id})
SET n:$(c.type)
SET n += c.fields
SET n.encoding = c.encoding, n.created_at = c.created_at, n.height = c.height,
    n.content_hash = c.content_hash, n.content_size = c.content_size,
    n.content = c.content`

// edgeCypher writes all edges of one relationship type. Neo4j evaluates $(...)
// once per query, not per UNWIND row, so the type is a literal here.
func edgeCypher(relType string) string {
	lit := "`" + strings.ReplaceAll(relType, "`", "``") + "`"
	return `
UNWIND $edges AS e
MATCH (n {id: e.src})
MERGE (tgt {id: e.reference})
MERGE (n)-[r:` + lit + ` {edge_id: e.edge_id}]->(tgt)
SET r += e.fields
SET r.encoding = e.encoding, r.direction = e.direction, r.content_hash = e.content_hash,
    r.content_size = e.content_size, r.content = e.content`
}

// PutClaims projects each claim into neo4j's native typed graph: a node labelled
// with the claim's type, its edges as typed relationships, legible inline content
// as a `content` property. Nodes MERGE by id, so re-puts are idempotent and a
// reference target stays a labelless stub until its own claim arrives.
func (u *neo4jUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	if len(cs) == 0 {
		return nil
	}
	// Diff overlays resolve before projecting: a delta's effective content_size
	// and encoding come from its base, and claimParam reads the materialised view.
	if err := u.materializeForWrite(ctx, cs); err != nil {
		return err
	}
	claims := make([]map[string]any, 0, len(cs))
	byType := map[string][]map[string]any{} // edge type → its edge params (each with src)
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		np := u.claimParam(c)
		edges, _ := np["edges"].([]map[string]any)
		delete(np, "edges") // the node query does not carry edges
		claims = append(claims, np)
		src := np["id"].(string)
		for _, ep := range edges {
			ep["src"] = src
			byType[ep["type"].(string)] = append(byType[ep["type"].(string)], ep)
		}
	}
	if _, err := u.query(ctx, cypherPutNodes, map[string]any{"claims": claims}); err != nil {
		return fmt.Errorf("%w: put nodes: %w", errQuery, err)
	}
	// One query per edge type (dynamic rel types aren't per-row, see edgeCypher).
	for relType, edges := range byType {
		if _, err := u.query(ctx, edgeCypher(relType), map[string]any{"edges": edges}); err != nil {
			return fmt.Errorf("%w: put edges (%s): %w", errQuery, relType, err)
		}
	}
	return nil
}

// cypherGetClaims fetches full claims by id — size(labels) > 0 tells a claim from
// a reference stub — with each node's labels and outgoing relationships.
const cypherGetClaims = `
UNWIND $ids AS id
MATCH (n {id: id})
WHERE size(labels(n)) > 0
RETURN properties(n) AS node, labels(n) AS labels,
       [(n)-[r]->(t) | {props: properties(r), rtype: type(r), ref: t.id}] AS edges`

// GetClaims reconstructs claims from their graph nodes and relationships,
// re-inlining the `content` property. An id absent from the cache is a miss
// (ranke.ErrNotFound), so the stack falls through to the durable layer.
func (u *neo4jUniverse) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	// Delta form is unsupported rather than a miss: a stack routes delta reads by
	// capability, so being asked is a caller's bug a fall-through would bury.
	if ranke.WantsDelta(opts...) {
		return nil, errDeltaForm
	}
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
	return out, nil
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

// GetClaimHeights returns each claim's committed height (§4.1) from the stored
// node property; an absent id is a miss (ranke.ErrNotFound).
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

// Query is implemented natively in query.go (Cypher traversal lowering).

// GetClaimsRaw always misses, routing the read to the authoritative byte layer.
func (u *neo4jUniverse) GetClaimsRaw(_ context.Context, ids []ranke.Id) ([][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("adapter/neo4j: stores no claim CBOR (structure-only cache): %w", ranke.ErrNotFound)
}

// GetContents always misses, routing the request to the durable layer. Inline
// content lives on the claim node and is served by GetClaims.
func (u *neo4jUniverse) GetContents(_ context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("adapter/neo4j: holds no external content (inline only, on the claim node): %w", ranke.ErrNotFound)
}

// PutContents is a no-op: PutClaims sets content on the claim node, and external
// content belongs to the durable layer.
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

// CopyClaims uses the ADT default walker.
func (u *neo4jUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, u, src, ids, opts...)
}

// CopyContents uses the ADT default walker.
func (u *neo4jUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, u, src, refs, opts...)
}

// --- Tags (Capabilities.Tags): mutable per-claim overlay ---
//
// Tags are node properties keyed with ranke.ReservedPrefix ("_"), which fixed
// claim properties avoid, so the tagger's keys (_br, _b_<branch>) store verbatim.
// GetClaims injects them back onto the claim; GetClaimTags is the shortcut.

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

// SetClaimsTags clears every tag matching a clearTags glob, then applies the new
// key→value pairs — one SET c += props, where a null value removes a property.
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
// answers queries natively (Cypher), and holds branch tags.
func (u *neo4jUniverse) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{
		Overwrite:   true,
		Delete:      true,
		Enumerate:   true,
		Persistent:  true,
		ReverseWalk: true, // holds edges both directions; Query lowers uses/connections to Cypher
		Tags:        true,
		Tier:        u.tier,
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
