package ranke

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multihash"
)

// NewFsUniverse returns a Universe backed by a single flat directory.
// Claims and content blobs share the same namespace, addressed by
// their id strings as filenames.
func NewFsUniverse(dir string) (Universe, error) {
	if dir == "" {
		return nil, errors.New("ranke.NewFsUniverse: empty directory path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ranke.NewFsUniverse: mkdir %s: %w", dir, err)
	}
	return &fsUniverse{dir: dir}, nil
}

type fsUniverse struct {
	dir string
}

func (u *fsUniverse) blobPath(id string) string { return filepath.Join(u.dir, id) }

func (u *fsUniverse) Close() error { return nil }

// encClaimFile is the on-disk shape for a single claim file.
type encClaimFile struct {
	Node  encNode   `cbor:"1,keyasint"`
	Edges []encEdge `cbor:"2,keyasint,omitempty"`
}

func (u *fsUniverse) LoadClaim(_ context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errors.New("ranke.fsUniverse.LoadClaim: nil id")
	}
	idStr := id.String()
	data, err := os.ReadFile(u.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read claim %s: %w", idStr, err)
	}
	return DecodeClaim(id, data)
}

func (u *fsUniverse) SaveClaim(_ context.Context, c Claim) error {
	cl, ok := c.(*claim)
	if !ok {
		return errors.New("ranke.fsUniverse.SaveClaim: foreign Claim implementation")
	}
	idStr := cl.node.id.String()
	path := u.blobPath(idStr)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	en, err := buildEncNode(cl.node)
	if err != nil {
		return err
	}
	ee := make([]encEdge, len(cl.edges))
	for i, e := range cl.edges {
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

func (u *fsUniverse) HasClaim(_ context.Context, id Id) (bool, error) {
	if id == nil {
		return false, nil
	}
	_, err := os.Stat(u.blobPath(id.String()))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (u *fsUniverse) GetContent(_ context.Context, hash Id, size uint64) ([]byte, error) {
	if hash == nil {
		return nil, errors.New("ranke.fsUniverse.GetContent: nil hash")
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return nil, fmt.Errorf("expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, fmt.Errorf("unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}
	idStr := hash.String()
	f, err := os.Open(u.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, size)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("content %s: truncated — got %d bytes, expected %d", idStr, n, size)
	}
	if err != nil {
		return nil, fmt.Errorf("content %s: read: %w", idStr, err)
	}
	var probe [1]byte
	if m, _ := f.Read(probe[:]); m > 0 {
		return nil, fmt.Errorf("content %s: file longer than expected %d bytes", idStr, size)
	}
	digest := sha256.Sum256(buf)
	if !bytes.Equal(digest[:], decoded.Digest) {
		return nil, fmt.Errorf("content %s: hash mismatch — bytes have been modified", idStr)
	}
	return buf, nil
}

func (u *fsUniverse) StreamContent(_ context.Context, hash Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errors.New("ranke.fsUniverse.StreamContent: nil hash")
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return nil, fmt.Errorf("expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, fmt.Errorf("unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}
	idStr := hash.String()
	f, err := os.Open(u.blobPath(idStr))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &verifyingReader{
		src:            f,
		hasher:         sha256.New(),
		expectedDigest: decoded.Digest,
		expectedSize:   size,
		id:             idStr,
	}, nil
}

func (u *fsUniverse) SaveContent(_ context.Context, hash Id, content []byte) error {
	if hash == nil {
		return errors.New("ranke.fsUniverse.SaveContent: nil hash")
	}
	path := u.blobPath(hash.String())
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return atomicWrite(path, content)
}

func (u *fsUniverse) MergeClaim(ctx context.Context, src Universe, id Id) error {
	return DefaultMergeClaim(ctx, u, src, id)
}

func (u *fsUniverse) HasContent(_ context.Context, hash Id) (bool, error) {
	if hash == nil {
		return false, nil
	}
	_, err := os.Stat(u.blobPath(hash.String()))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// verifyingReader holds back the final block until both the overflow
// probe and the hash check pass, so a consumer's final Read returns
// (0, error) on integrity failure instead of a clean io.EOF over
// corrupted content.
type verifyingReader struct {
	src            io.ReadCloser
	hasher         hashWriter
	expectedDigest []byte
	expectedSize   uint64
	read           uint64
	done           bool
	id             string
}

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

// DecodeClaim decodes the on-disk CBOR bytes of a single claim into
// a Claim with id set. Exposed for tooling that inspects archive
// files directly (the ranke CLI) — returns an error when the bytes
// aren't a valid claim, which the CLI uses to dispatch claim vs
// content.
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
