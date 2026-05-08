package ranke

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

// NewFsArchive opens (or creates) a filesystem-backed Archive at dir.
//
// Layout — separate subdirs for transparency, since the FS backend
// is intended for development and inspection. Production backends
// (S3, IPFS, ...) can store flat if they prefer; only this backend
// commits to the layout below.
//
//	dir/
//	├── B_h                // current contribution/branches claim id (text)
//	├── claims/<id>        // canonical CBOR of a claim
//	└── content/<id>       // raw content bytes
//
// On open: dir + subdirs are created if missing; B_h is read eagerly
// if present; claims and content are fetched lazily on first reference.
//
// Reload: drop this Archive and call NewFsArchive(dir) again. Caches
// reset; persisted state on disk is the source of truth. Returned
// values from the previous handle remain valid (self-contained).
func NewFsArchive(dir string) (Archive, error) {
	if dir == "" {
		return nil, errors.New("ranke.NewFsArchive: empty directory path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ranke.NewFsArchive: mkdir %s: %w", dir, err)
	}
	for _, sub := range []string{"claims", "content"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("ranke.NewFsArchive: mkdir %s/%s: %w", dir, sub, err)
		}
	}

	branchesHead, err := loadBranchesHeadFile(dir)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewFsArchive: load B_h: %w", err)
	}

	return &archive{
		claims:       make(map[string]*claim),
		content:      make(map[string][]byte),
		branchesHead: branchesHead,
		backend:      &fsBackend{dir: dir},
	}, nil
}

// --- fsBackend ---

// fsBackend implements archiveBackend over the filesystem.
type fsBackend struct {
	dir string
}

func (b *fsBackend) claimPath(id string) string   { return filepath.Join(b.dir, "claims", id) }
func (b *fsBackend) contentPath(id string) string { return filepath.Join(b.dir, "content", id) }
func (b *fsBackend) bhPath() string               { return filepath.Join(b.dir, "B_h") }

// encClaimFile is the on-disk shape for a single claim file: the
// node plus the full edge records. Not part of any canonical-hash
// path — used for storage only.
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

	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, fmt.Errorf("decode node %s: %w", idStr, err)
	}
	id, err := ParseId(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id %s: %w", idStr, err)
	}
	n.id = id

	edges := make([]*edge, len(ec.Edges))
	for i, ee := range ec.Edges {
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, fmt.Errorf("decode edge %d of %s: %w", i, idStr, err)
		}
		if i < len(n.edges) {
			e.id = n.edges[i]
		}
		edges[i] = e
	}

	return &claim{node: n, edges: edges}, nil
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

func (b *fsBackend) saveBranchesHead(id Id) error {
	if id == nil {
		return os.Remove(b.bhPath()) // no branches: remove the handle file
	}
	return atomicWrite(b.bhPath(), []byte(id.String()))
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

// loadBranchesHeadFile reads B_h into an Id, or returns nil if the
// file doesn't exist (fresh archive).
func loadBranchesHeadFile(dir string) (Id, error) {
	path := filepath.Join(dir, "B_h")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseId(strings.TrimSpace(string(data)))
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

// decodeEdge rebuilds an *edge from its on-disk encoding. The edge's
// id is set by the caller (from the corresponding entry in node.edges).
// Edge content is inline in the wire form, so it's restored directly.
func decodeEdge(ee encEdge) (*edge, error) {
	ref, err := hashFromBytes(ee.Reference)
	if err != nil {
		return nil, err
	}
	return &edge{
		reference:         ref,
		typeClass:         EdgeClass(ee.TypeClass),
		typeSub:           ee.TypeSub,
		content:           ee.Content,
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            ee.Fields,
	}, nil
}
