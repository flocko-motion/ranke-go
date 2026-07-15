// package: neo4j / persistence-cache
// type:    adapter
// job:     a graph-native CACHE Universe on neo4j — stores claim structure (nodes, edges) so closure/membership run as native Cypher instead of edge walks
// limits:  pure cache — no canonical CBOR, no external content, inline content only up to a cap (default 4 KiB); stack over a durable Universe (-> adapter/s3, adapter/fs) that holds the bytes and serves content misses
//
// Package neo4j is the first pure-caching ranke Universe. It stores each claim
// as a graph node with its edges as :REFERENCES relationships, so closure and
// membership queries run as native Cypher instead of the ADT's reference-edge
// walk.
//
// It deliberately does NOT store the canonical CBOR, external content, or
// inline content beyond WithContentCap (default 4 KiB). A claim's id depends
// only on content_hash + content_size — never the content bytes — so claims
// reconstruct id-faithfully from the graph via ranke.AssembleClaim; content
// this cache does not hold reconstructs as external and is served by the
// durable Universe this cache is stacked over.
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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/flocko-motion/ranke-go"
)

// defaultContentCap is the largest inline content the cache stores inline.
const defaultContentCap = 4 << 10 // 4 KiB

// New returns a graph-native cache Universe over an already-configured driver.
// Connection, auth, and (unless WithDatabase is given) the target database
// live on the driver.
func New(driver neo4jdriver.DriverWithContext, opts ...Option) ranke.Universe {
	u := &neo4jUniverse{driver: driver, contentCap: defaultContentCap}
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

type neo4jUniverse struct {
	driver     neo4jdriver.DriverWithContext
	database   string
	contentCap int
}

// Compile-time proof the adapter satisfies the full Universe contract.
var (
	_ ranke.Universe = (*neo4jUniverse)(nil)
	_ ranke.Tagger   = (*neo4jUniverse)(nil)
)

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

const cypherPutContents = `
UNWIND $contents AS ct
MERGE (co:` + labelContent + ` {hash: ct.hash})
SET co.size = ct.size, co.b64 = ct.b64`

const cypherPutClaims = `
UNWIND $claims AS c
MERGE (n:` + labelClaim + ` {id: c.id})
SET n.type = c.type, n.encoding = c.encoding, n.created_at = c.created_at,
    n.height = c.height,
    n.content_hash = c.content_hash, n.content_size = c.content_size,
    n.field_keys = c.field_keys, n.field_vals = c.field_vals
WITH n, c
UNWIND c.edges AS e
MERGE (t:` + labelClaim + ` {id: e.reference})
MERGE (n)-[r:` + relReferences + ` {edge_id: e.edge_id}]->(t)
SET r.type = e.type, r.direction = e.direction, r.content_hash = e.content_hash,
    r.content_size = e.content_size, r.field_keys = e.field_keys, r.field_vals = e.field_vals`

// PutClaims caches each claim's structure — a :Claim node with its edges as
// :REFERENCES relationships — and any inline content within cap as :Content
// nodes. The canonical CBOR is not stored. Idempotent: nodes and edges MERGE
// on id (claims are immutable, so a re-put writes identical structure).
func (u *neo4jUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	if len(cs) == 0 {
		return nil
	}
	claims := make([]map[string]any, 0, len(cs))
	var contents []map[string]any
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		cp, cts := u.claimParam(c)
		claims = append(claims, cp)
		contents = append(contents, cts...)
	}
	if len(contents) > 0 {
		if _, err := u.query(ctx, cypherPutContents, map[string]any{"contents": contents}); err != nil {
			return fmt.Errorf("%w: put contents: %w", errQuery, err)
		}
	}
	if _, err := u.query(ctx, cypherPutClaims, map[string]any{"claims": claims}); err != nil {
		return fmt.Errorf("%w: put claims: %w", errQuery, err)
	}
	return nil
}

const cypherGetClaims = `
UNWIND $ids AS id
MATCH (n:` + labelClaim + ` {id: id})
WHERE n.type IS NOT NULL
RETURN properties(n) AS node,
       [(n)-[r:` + relReferences + `]->(t) | {props: properties(r), ref: t.id}] AS edges`

const cypherGetContents = `
UNWIND $hashes AS h
MATCH (co:` + labelContent + ` {hash: h})
RETURN co.hash AS hash, co.b64 AS b64`

// GetClaims reconstructs claims from their graph nodes (+ edge relationships),
// re-inlining any content the cache holds and materialising diff overlays like
// any Universe. A requested id absent from the cache is a miss
// (ranke.ErrNotFound) so the stack can fall through to the durable layer.
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
		props map[string]any
		edges []any
	}
	byID := make(map[string]rec, len(res.Records))
	hashes := make(map[string]struct{})
	for _, r := range res.Records {
		props, _ := valOf(r, "node").(map[string]any)
		edges, _ := valOf(r, "edges").([]any)
		byID[asString(props["id"])] = rec{props, edges}
		collectHash(hashes, props["content_hash"])
		for _, e := range edges {
			if em, ok := e.(map[string]any); ok {
				if ep, ok := em["props"].(map[string]any); ok {
					collectHash(hashes, ep["content_hash"])
				}
			}
		}
	}

	content, err := u.fetchContents(ctx, hashes)
	if err != nil {
		return nil, err
	}

	for i, id := range ids {
		r, ok := byID[id.String()]
		if !ok {
			return nil, fmt.Errorf("claim %s: %w", id, ranke.ErrNotFound)
		}
		parts, err := partsFromNode(id, r.props, r.edges, content)
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

// fetchContents loads the inline bytes the cache holds for the given hashes,
// decoding the stored base64. Hashes it lacks are simply absent from the map.
func (u *neo4jUniverse) fetchContents(ctx context.Context, hashes map[string]struct{}) (map[string][]byte, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	list := make([]string, 0, len(hashes))
	for h := range hashes {
		list = append(list, h)
	}
	res, err := u.query(ctx, cypherGetContents, map[string]any{"hashes": list})
	if err != nil {
		return nil, fmt.Errorf("%w: get contents: %w", errQuery, err)
	}
	out := make(map[string][]byte, len(res.Records))
	for _, r := range res.Records {
		b, err := base64.StdEncoding.DecodeString(asString(valOf(r, "b64")))
		if err != nil {
			return nil, err
		}
		out[asString(valOf(r, "hash"))] = b
	}
	return out, nil
}

const cypherHasClaims = `
UNWIND $ids AS id
OPTIONAL MATCH (n:` + labelClaim + ` {id: id})
RETURN id AS id, (n IS NOT NULL AND n.type IS NOT NULL) AS has`

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
MATCH (n:` + labelClaim + ` {id: id})
WHERE n.type IS NOT NULL
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

// GetClaimsRaw always misses: a structure/query cache stores no CBOR. The
// ErrNotFound lets a stack route the request to the authoritative byte layer.
func (u *neo4jUniverse) GetClaimsRaw(_ context.Context, ids []ranke.Id) ([][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("adapter/neo4j: stores no claim CBOR (structure-only cache): %w", ranke.ErrNotFound)
}

// GetContents returns inline content the cache holds (≤ cap); a hash it lacks
// (external or over-cap content) is a miss (ranke.ErrNotFound) so the stack
// falls through to the durable layer.
func (u *neo4jUniverse) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	hashes := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Hash == nil {
			return nil, errNilHash
		}
		hashes[ref.Hash.String()] = struct{}{}
	}
	content, err := u.fetchContents(ctx, hashes)
	if err != nil {
		return nil, err
	}
	for i, ref := range refs {
		b, ok := content[ref.Hash.String()]
		if !ok {
			return nil, fmt.Errorf("content %s: %w", ref.Hash, ranke.ErrNotFound)
		}
		if err := ranke.VerifyContent(ref.Hash, ref.ContentSize, b); err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// PutContents caches only inline content ≤ cap; larger content is skipped
// silently (the durable layer holds it) rather than erroring.
func (u *neo4jUniverse) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	if len(blobs) == 0 {
		return nil
	}
	contents := make([]map[string]any, 0, len(blobs))
	for _, bl := range blobs {
		if bl.Hash == nil {
			return errNilHash
		}
		if u.contentCap == 0 || len(bl.Content) == 0 || len(bl.Content) > u.contentCap {
			continue // over cap / empty / structure-only: the lower layer keeps it
		}
		contents = append(contents, map[string]any{
			"hash": bl.Hash.String(),
			"size": int64(len(bl.Content)),
			"b64":  base64.StdEncoding.EncodeToString(bl.Content),
		})
	}
	if len(contents) == 0 {
		return nil
	}
	if _, err := u.query(ctx, cypherPutContents, map[string]any{"contents": contents}); err != nil {
		return fmt.Errorf("%w: put contents: %w", errQuery, err)
	}
	return nil
}

const cypherHasContents = `
UNWIND $hashes AS h
OPTIONAL MATCH (co:` + labelContent + ` {hash: h})
RETURN h AS hash, co IS NOT NULL AS has`

// HasContents reports which hashes the cache holds inline.
func (u *neo4jUniverse) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	hStrs := make([]string, len(hashes))
	pos := make(map[string]int, len(hashes))
	for i, h := range hashes {
		if h == nil {
			return nil, errNilHash
		}
		hStrs[i] = h.String()
		pos[h.String()] = i
	}
	res, err := u.query(ctx, cypherHasContents, map[string]any{"hashes": hStrs})
	if err != nil {
		return nil, fmt.Errorf("%w: has contents: %w", errQuery, err)
	}
	for _, r := range res.Records {
		if i, ok := pos[asString(valOf(r, "hash"))]; ok {
			out[i], _ = valOf(r, "has").(bool)
		}
	}
	return out, nil
}

// StreamContent streams cached inline content (≤ cap); a miss for anything the
// cache does not hold.
func (u *neo4jUniverse) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errNilHash
	}
	content, err := u.fetchContents(ctx, map[string]struct{}{hash.String(): {}})
	if err != nil {
		return nil, err
	}
	b, ok := content[hash.String()]
	if !ok {
		return nil, fmt.Errorf("content %s: %w", hash, ranke.ErrNotFound)
	}
	return ranke.NewVerifyingReader(io.NopCloser(bytes.NewReader(b)), hash, size)
}

const cypherInClosure = `
RETURN EXISTS {
  MATCH (h:` + labelClaim + `)-[:` + relReferences + `*0..]->(t:` + labelClaim + ` {id: $id})
  WHERE h.id IN $heads
} AS inClosure`

// InClosure reports whether id is reachable from any head — answered natively
// by a variable-length path over :REFERENCES, the reason this cache exists.
func (u *neo4jUniverse) InClosure(ctx context.Context, heads []ranke.Id, id ranke.Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	headStrs := make([]string, 0, len(heads))
	for _, h := range heads {
		if h != nil {
			headStrs = append(headStrs, h.String())
		}
	}
	if len(headStrs) == 0 {
		return false, nil
	}
	res, err := u.query(ctx, cypherInClosure, map[string]any{"heads": headStrs, "id": id.String()})
	if err != nil {
		return false, fmt.Errorf("%w: in closure: %w", errQuery, err)
	}
	if len(res.Records) == 0 {
		return false, nil
	}
	in, _ := valOf(res.Records[0], "inClosure").(bool)
	return in, nil
}

// GetFromClosure returns the claim at id if it is reachable from any head,
// else ranke.ErrNotFound.
func (u *neo4jUniverse) GetFromClosure(ctx context.Context, heads []ranke.Id, id ranke.Id) (ranke.Claim, error) {
	in, err := u.InClosure(ctx, heads, id)
	if err != nil {
		return nil, err
	}
	if !in {
		return nil, fmt.Errorf("claim %s: %w", id, ranke.ErrNotFound)
	}
	cs, err := u.GetClaims(ctx, []ranke.Id{id})
	if err != nil {
		return nil, err
	}
	return cs[0], nil
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

// --- Tagger: branch-membership tags (Capabilities.Tags) ---
//
// Tags are mutable node properties keyed _b_<branch>; the _b_ prefix keeps
// user branch names from colliding with fixed system keys. They are a pure-
// functional overlay — never part of the claim, and (since claim fields live
// in field_keys/field_vals arrays, not node properties) never read back into
// ClaimParts.

func branchTagKey(branch string) string { return "_b_" + branch }

const cypherTagBranch = `
MATCH (h:` + labelClaim + ` {id: $head})
WHERE h[$key] IS NULL
MATCH (h) (()-[:` + relReferences + `]->(m:` + labelClaim + `) WHERE m[$key] IS NULL)* (c:` + labelClaim + `)
SET c[$key] = $rev`

// TagBranch stamps _b_<branch>=revision across head's closure in one query,
// pruned at tagged claims: the quantified path only extends through untagged
// nodes (WHERE m[$key] IS NULL), so it touches just the delta — already-tagged
// claims (and their closures, tagged too) are never revisited. A head that is
// already tagged matches nothing and is a no-op. Call oldest→newest.
func (u *neo4jUniverse) TagBranch(ctx context.Context, branch string, head ranke.Id, revision uint64) error {
	if head == nil {
		return errNilID
	}
	_, err := u.query(ctx, cypherTagBranch, map[string]any{
		"head": head.String(), "key": branchTagKey(branch), "rev": int64(revision),
	})
	if err != nil {
		return fmt.Errorf("%w: tag branch: %w", errQuery, err)
	}
	return nil
}

const cypherSetBranchRevision = `MATCH (c:` + labelClaim + ` {id: $id}) SET c[$key] = $rev`

func (u *neo4jUniverse) SetBranchRevision(ctx context.Context, claim ranke.Id, branch string, revision uint64) error {
	if claim == nil {
		return errNilID
	}
	_, err := u.query(ctx, cypherSetBranchRevision, map[string]any{
		"id": claim.String(), "key": branchTagKey(branch), "rev": int64(revision),
	})
	if err != nil {
		return fmt.Errorf("%w: set branch revision: %w", errQuery, err)
	}
	return nil
}

const cypherBranchRevision = `MATCH (c:` + labelClaim + ` {id: $id}) RETURN c[$key] AS rev`

func (u *neo4jUniverse) BranchRevision(ctx context.Context, claim ranke.Id, branch string) (uint64, bool, error) {
	if claim == nil {
		return 0, false, errNilID
	}
	res, err := u.query(ctx, cypherBranchRevision, map[string]any{
		"id": claim.String(), "key": branchTagKey(branch),
	})
	if err != nil {
		return 0, false, fmt.Errorf("%w: branch revision: %w", errQuery, err)
	}
	if len(res.Records) == 0 || valOf(res.Records[0], "rev") == nil {
		return 0, false, nil
	}
	return uint64(asInt(valOf(res.Records[0], "rev"))), true, nil
}

// Capabilities: a graph DB can overwrite, delete, and enumerate, is durable,
// exposes a GQL (Cypher) query surface, and holds branch tags.
func (u *neo4jUniverse) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{
		Overwrite:  true,
		Delete:     true,
		Enumerate:  true,
		Persistent: true,
		GQL:        true,
		Tags:       true,
	}
}

// Close is a no-op: the caller owns the driver's lifecycle.
func (u *neo4jUniverse) Close() error { return nil }

// valOf reads a named column from a record (nil when absent).
func valOf(r *neo4jdriver.Record, key string) any {
	v, _ := r.Get(key)
	return v
}

// collectHash adds a non-empty id string to the set.
func collectHash(set map[string]struct{}, v any) {
	if s := asString(v); s != "" {
		set[s] = struct{}{}
	}
}
