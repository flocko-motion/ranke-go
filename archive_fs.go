package ranke

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multihash"
)

// NewFsArchive opens (or creates) a filesystem-backed Archive at dir.
//
// Layout — single flat directory, file per id. The paper treats U as
// one content-addressed store; claims and content live in the same
// namespace, distinguished only by what's inside each file (CBOR
// claim vs raw content bytes). Practical collision is astronomical
// — both ids are SHA2-256-rooted multihashes / multikey signatures.
//
//	dir/
//	├── B_h            // current contribution/branches claim id (text)
//	└── <id>           // either a CBOR-encoded claim or raw content
//
// On open: dir is created if missing; B_h is read eagerly if present;
// claims and content are fetched lazily on first reference.
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

// blobPath returns the on-disk path for any id in the unified store.
// Claims and content share the same flat directory; dispatch on the
// file's bytes happens at read time (loadClaim attempts CBOR; if
// that fails the caller treats the file as content).
func (b *fsBackend) blobPath(id string) string  { return filepath.Join(b.dir, id) }
func (b *fsBackend) bhPath() string             { return filepath.Join(b.dir, "B_h") }

// encClaimFile is the on-disk shape for a single claim file: the
// node plus the full edge records. Not part of any canonical-hash
// path — used for storage only.
type encClaimFile struct {
	Node  encNode   `cbor:"1,keyasint"`
	Edges []encEdge `cbor:"2,keyasint,omitempty"`
}

func (b *fsBackend) loadClaim(idStr string) (*claim, error) {
	data, err := os.ReadFile(b.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("read claim %s: %w", idStr, err)
	}
	id, err := ParseId(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id %s: %w", idStr, err)
	}
	cl, err := DecodeClaim(id, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", idStr, err)
	}
	return cl.(*claim), nil
}

// DecodeClaim decodes the on-disk CBOR bytes of a single claim into
// a Claim value with id set. Useful for dev tools (the ranke CLI)
// that want to inspect individual archive files without opening the
// archive. Returns an error if b doesn't decode as a claim's
// canonical CBOR — the CLI uses that to dispatch claim-vs-content.
func DecodeClaim(id Id, b []byte) (Claim, error) {
	var ec encClaimFile
	if err := cbor.Unmarshal(b, &ec); err != nil {
		return nil, fmt.Errorf("DecodeClaim: %w", err)
	}
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, fmt.Errorf("DecodeClaim: node: %w", err)
	}
	n.id = id
	edges := make([]*edge, len(ec.Edges))
	for i, ee := range ec.Edges {
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, fmt.Errorf("DecodeClaim: edge %d: %w", i, err)
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
	path := b.blobPath(idStr)
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

func (b *fsBackend) getContent(expectedHash Id, expectedSize uint64) ([]byte, error) {
	if expectedHash == nil {
		return nil, errors.New("loadContent: nil expectedHash")
	}
	decoded, err := multihash.Decode(idBytes(expectedHash))
	if err != nil {
		return nil, fmt.Errorf("loadContent: expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, fmt.Errorf("loadContent: unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}

	idStr := expectedHash.String()
	f, err := os.Open(b.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	defer f.Close()

	// Bounded streaming read: we allocate exactly expectedSize bytes
	// and read at most expectedSize+1 from disk. A maliciously
	// enlarged blob is rejected after touching expectedSize+1 bytes,
	// long before it could OOM the process; truncation is caught by
	// the short-read branch.
	buf := make([]byte, expectedSize)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("content %s: truncated — got %d bytes, expected %d", idStr, n, expectedSize)
	}
	if err != nil {
		return nil, fmt.Errorf("content %s: read: %w", idStr, err)
	}
	var probe [1]byte
	if m, _ := f.Read(probe[:]); m > 0 {
		return nil, fmt.Errorf("content %s: file longer than expected %d bytes", idStr, expectedSize)
	}

	// Hash check (the second half of "verified content"): the bytes
	// we just read must hash to expectedHash. Past here, the caller
	// can trust the returned bytes against the claim's signed
	// (content_hash, size) pair.
	digest := sha256.Sum256(buf)
	if !bytes.Equal(digest[:], decoded.Digest) {
		return nil, fmt.Errorf("content %s: hash mismatch — bytes have been modified", idStr)
	}
	return buf, nil
}

func (b *fsBackend) streamContent(expectedHash Id, expectedSize uint64) (io.ReadCloser, error) {
	if expectedHash == nil {
		return nil, errors.New("streamContent: nil expectedHash")
	}
	decoded, err := multihash.Decode(idBytes(expectedHash))
	if err != nil {
		return nil, fmt.Errorf("streamContent: expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, fmt.Errorf("streamContent: unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}
	idStr := expectedHash.String()
	f, err := os.Open(b.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	return &verifyingReader{
		src:            f,
		hasher:         sha256.New(),
		expectedDigest: decoded.Digest,
		expectedSize:   expectedSize,
		id:             idStr,
	}, nil
}

// verifyingReader streams content from src, hashing every byte and
// bounding reads to expectedSize. The Read call that would consume
// the final expected byte first probes src for any trailing bytes
// (overflow) and finalizes the hash check before releasing those
// bytes — so the consumer's final Read returns either (bytes, EOF)
// on success or (0, error) on integrity failure, never a clean EOF
// over corrupted content.
type verifyingReader struct {
	src            io.ReadCloser
	hasher         hashWriter
	expectedDigest []byte
	expectedSize   uint64
	read           uint64
	done           bool
	id             string
}

// hashWriter is the slice of hash.Hash we actually use, avoids
// pulling in an alias import just for one type.
type hashWriter interface {
	io.Writer
	Sum(b []byte) []byte
}

func (vr *verifyingReader) Read(p []byte) (int, error) {
	if vr.done {
		return 0, io.EOF
	}
	remaining := vr.expectedSize - vr.read
	if remaining == 0 {
		vr.done = true
		return 0, io.EOF
	}
	if uint64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := vr.src.Read(p)
	if n > 0 {
		vr.hasher.Write(p[:n])
		vr.read += uint64(n)
	}
	if vr.read == vr.expectedSize {
		// Final-block check: peek for overflow, finalize hash. Only
		// release these bytes if both pass.
		var probe [1]byte
		m, probeErr := vr.src.Read(probe[:])
		vr.done = true
		if m > 0 {
			return 0, fmt.Errorf("content %s: file longer than expected %d bytes", vr.id, vr.expectedSize)
		}
		if probeErr != nil && probeErr != io.EOF {
			return 0, probeErr
		}
		if !bytes.Equal(vr.hasher.Sum(nil), vr.expectedDigest) {
			return 0, fmt.Errorf("content %s: hash mismatch — bytes have been modified", vr.id)
		}
		return n, io.EOF
	}
	if err == io.EOF {
		vr.done = true
		return n, fmt.Errorf("content %s: truncated — got %d bytes, expected %d", vr.id, vr.read, vr.expectedSize)
	}
	return n, err
}

func (vr *verifyingReader) Close() error { return vr.src.Close() }

func (b *fsBackend) saveContent(idStr string, data []byte) error {
	path := b.blobPath(idStr)
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
		title:         en.Title,
		createdAt:     createdAt,
		fields:        en.Fields,
		pubkey:        en.Pubkey,
	}
	if len(en.ContentHash) > 0 {
		ch, err := hashFromMultihashBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
		n.size = en.Size
	}
	if len(en.Edges) > 0 {
		n.edges = make([]Id, len(en.Edges))
		for i, raw := range en.Edges {
			h, err := idFromBytes(raw)
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
	ref, err := idFromBytes(ee.Reference)
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
