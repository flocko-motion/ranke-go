package ranke

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fxamacker/cbor/v2"
)

// NewFsStore opens (or creates) a filesystem-backed Store at dir.
//
// Layout:
//
//	dir/
//	├── branches.json                 // name → id-string
//	├── claims/<id-string>            // CBOR-encoded encClaimFile
//	└── content/<id-string>           // raw content bytes
//
// On open: dir is created if missing; subdirs created on demand;
// branches.json is read eagerly into memory (small). Claims and
// content are fetched lazily from disk on first reference and
// cached in memory for the lifetime of this Store handle.
//
// Reload: drop this Store and call NewFsStore(dir) again. Caches
// reset; persisted state on disk is the source of truth. Returned
// values from the previous handle remain valid (self-contained).
func NewFsStore(dir string) (Store, error) {
	if dir == "" {
		return nil, errors.New("ranke.NewFsStore: empty directory path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ranke.NewFsStore: mkdir %s: %w", dir, err)
	}
	for _, sub := range []string{"claims", "content"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("ranke.NewFsStore: mkdir %s/%s: %w", dir, sub, err)
		}
	}

	branches, err := loadBranchesFile(dir)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewFsStore: load branches: %w", err)
	}

	return &store{
		claims:   make(map[string]*claim),
		content:  make(map[string][]byte),
		branches: branches,
		backend:  &fsBackend{dir: dir},
	}, nil
}

// --- fsBackend ---

// fsBackend implements storeBackend over the filesystem.
type fsBackend struct {
	dir string
}

func (b *fsBackend) claimPath(id string) string {
	return filepath.Join(b.dir, "claims", id)
}

func (b *fsBackend) contentPath(id string) string {
	return filepath.Join(b.dir, "content", id)
}

func (b *fsBackend) branchesPath() string {
	return filepath.Join(b.dir, "branches.json")
}

// encClaimFile is the on-disk shape for a single claim: the node
// plus the full edge records. Not part of any canonical-hash path —
// used for storage only.
type encClaimFile struct {
	Node  encNode   `cbor:"1,keyasint"`
	Edges []encEdge `cbor:"2,keyasint,omitempty"`
}

func (b *fsBackend) loadClaim(idStr string) (*claim, error) {
	data, err := os.ReadFile(b.claimPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("read claim %s: %w", idStr, err)
	}
	var ec encClaimFile
	if err := cbor.Unmarshal(data, &ec); err != nil {
		return nil, fmt.Errorf("decode claim %s: %w", idStr, err)
	}

	// Reconstruct node.
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, fmt.Errorf("decode node %s: %w", idStr, err)
	}
	id, err := ParseId(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id %s: %w", idStr, err)
	}
	n.id = id

	// Reconstruct edges. Their ids are in n.edges (canonical sort
	// order); we trust the on-disk pairing of node+edges.
	edges := make([]*edge, len(ec.Edges))
	for i, ee := range ec.Edges {
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, fmt.Errorf("decode edge %d of %s: %w", i, idStr, err)
		}
		// Edge id: matches the corresponding entry in n.edges.
		if i < len(n.edges) {
			e.id = n.edges[i]
		}
		edges = append(edges[:i], e)
	}
	if len(edges) > len(ec.Edges) {
		edges = edges[:len(ec.Edges)]
	}

	c := &claim{node: n, edges: edges}
	return c, nil
}

func (b *fsBackend) saveClaim(c *claim) error {
	idStr := c.node.id.String()
	path := b.claimPath(idStr)
	if _, err := os.Stat(path); err == nil {
		return nil // already on disk; immutable
	}

	en, err := buildEncNode(c.node)
	if err != nil {
		return err
	}
	ee := make([]encEdge, len(c.edges))
	for i, e := range c.edges {
		ee[i], err = buildEncEdge(e)
		if err != nil {
			return err
		}
	}
	data, err := encodingMode.Marshal(encClaimFile{Node: en, Edges: ee})
	if err != nil {
		return fmt.Errorf("encode claim %s: %w", idStr, err)
	}
	return atomicWrite(path, data)
}

func (b *fsBackend) loadContent(idStr string) ([]byte, error) {
	data, err := os.ReadFile(b.contentPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	return data, nil
}

func (b *fsBackend) saveContent(idStr string, data []byte) error {
	path := b.contentPath(idStr)
	if _, err := os.Stat(path); err == nil {
		return nil // already on disk; immutable
	}
	return atomicWrite(path, data)
}

func (b *fsBackend) saveBranches(branches map[string]Id) error {
	flat := make(map[string]string, len(branches))
	for k, v := range branches {
		flat[k] = v.String()
	}
	data, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		return fmt.Errorf("encode branches.json: %w", err)
	}
	return atomicWrite(b.branchesPath(), data)
}

// --- helpers ---

// atomicWrite writes data to path via a tmp file + rename. Safe
// against partial writes on crash.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// loadBranchesFile reads branches.json into a name → Id map. Returns
// an empty map if the file doesn't exist (fresh store).
func loadBranchesFile(dir string) (map[string]Id, error) {
	path := filepath.Join(dir, "branches.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]Id), nil
		}
		return nil, err
	}
	flat := make(map[string]string)
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("parse branches.json: %w", err)
	}
	out := make(map[string]Id, len(flat))
	for k, v := range flat {
		id, err := ParseId(v)
		if err != nil {
			return nil, fmt.Errorf("branches.json: parse id for %q: %w", k, err)
		}
		out[k] = id
	}
	return out, nil
}

// decodeNode rebuilds a *node from its on-disk encoding. id is set
// by the caller (from the filename, then verified externally if
// desired).
func decodeNode(en encNode) (*node, error) {
	createdAt, err := parseRFC3339Nano(en.CreatedAt)
	if err != nil {
		return nil, err
	}
	n := &node{
		typeClass:     NodeClass(en.TypeClass),
		typeSub:       en.TypeSub,
		encodingClass: EncodingClass(en.EncodingClass),
		encodingSub:   en.EncodingSub,
		createdAt:     createdAt,
		fields:        en.Fields,
	}
	if len(en.ContentHash) > 0 {
		ch, err := hashFromBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
	}
	if len(en.Edges) > 0 {
		n.edges = make([]Id, len(en.Edges))
		for i, raw := range en.Edges {
			h, err := hashFromBytes(raw)
			if err != nil {
				return nil, err
			}
			n.edges[i] = h
		}
	}
	return n, nil
}

// decodeEdge rebuilds a *edge from its on-disk encoding. The edge's
// id is set by the caller (from the corresponding entry in node.edges).
func decodeEdge(ee encEdge) (*edge, error) {
	ref, err := hashFromBytes(ee.Reference)
	if err != nil {
		return nil, err
	}
	e := &edge{
		reference:         ref,
		typeClass:         EdgeClass(ee.TypeClass),
		typeSub:           ee.TypeSub,
		encodingClass:     EncodingClass(ee.EncodingClass),
		encodingSub:       ee.EncodingSub,
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            ee.Fields,
	}
	if len(ee.ContentHash) > 0 {
		ch, err := hashFromBytes(ee.ContentHash)
		if err != nil {
			return nil, err
		}
		e.contentHash = ch
	}
	return e, nil
}
