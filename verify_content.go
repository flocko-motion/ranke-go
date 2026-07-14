// package: ranke / content
// type:    crypto
// job:     storage-agnostic content integrity (§5.10) — verify bytes against a hash+size, whole or streamed
// limits:  does not store or fetch content (-> universe); does not hash claim records (-> hash)
package ranke

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/multiformats/go-multihash"
)

// Content integrity (§5.10) — storage-agnostic. Content is addressed by
// the multihash of its bytes, so verification is a property of the bytes,
// not of where they live. Persistence adapters read raw bytes from their
// backing store and lean on these helpers to verify them; they never
// reimplement the hash scheme.

// VerifyContent checks that data is exactly the content addressed by hash
// and has the expected size. Returns ErrIntegrity on a size or digest
// mismatch.
func VerifyContent(hash Id, size uint64, data []byte) error {
	if hash == nil {
		return errNilHash
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return fmt.Errorf("ranke.VerifyContent: expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return fmt.Errorf("ranke.VerifyContent: unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}
	if uint64(len(data)) != size {
		return fmt.Errorf("%w: content %s size mismatch — got %d bytes, expected %d", ErrIntegrity, hash.String(), len(data), size)
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(digest[:], decoded.Digest) {
		return fmt.Errorf("%w: content %s hash mismatch — bytes have been modified", ErrIntegrity, hash.String())
	}
	return nil
}

// NewVerifyingReader wraps src so that a consumer reading to EOF also
// verifies the streamed bytes against hash and size (§5.10): the final
// Read returns an integrity error instead of a clean io.EOF if the bytes
// are truncated, over-long, or modified. For adapters that stream large
// content without buffering it whole.
func NewVerifyingReader(src io.ReadCloser, hash Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errNilHash
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return nil, fmt.Errorf("ranke.NewVerifyingReader: expected hash not a multihash: %w", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, fmt.Errorf("ranke.NewVerifyingReader: unsupported hash algorithm %s (only sha2-256)", decoded.Name)
	}
	return &verifyingReader{
		src:            src,
		hasher:         sha256.New(),
		expectedDigest: decoded.Digest,
		expectedSize:   size,
		id:             hash.String(),
	}, nil
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
