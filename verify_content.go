// package: ranke / content
// type:    crypto
// job:     storage-agnostic content integrity (§5.10) — verify bytes against a hash+size, whole or streamed
// limits:  does not store or fetch content (-> universe); does not hash claim records (-> hash)
package ranke

import (
	"bytes"
	"crypto/sha256"
	"io"
	"strconv"

	"github.com/multiformats/go-multihash"
)

// Content integrity (§5.10): content is addressed by the multihash of its bytes, so
// verification is a property of the bytes alone, wherever an adapter read them.

// VerifyContent checks data against the content hash addresses and the expected
// size, answering ErrIntegrity on either mismatch.
func VerifyContent(hash Id, size uint64, data []byte) error {
	if hash == nil {
		return errNilHash
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return WrapDetail(errVerifyContentOp, "expected hash not a multihash", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return WithDetail(errVerifyContentOp, "unsupported hash algorithm "+decoded.Name+" (only sha2-256)")
	}
	if uint64(len(data)) != size {
		return WithDetail(ErrIntegrity, "content "+hash.String()+" size mismatch — got "+strconv.Itoa(len(data))+" bytes, expected "+strconv.FormatUint(size, 10))
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(digest[:], decoded.Digest) {
		return WithDetail(ErrIntegrity, "content "+hash.String()+" hash mismatch — bytes have been modified")
	}
	return nil
}

// NewVerifyingReader wraps src so reading to EOF also verifies the stream (§5.10):
// the final Read answers an integrity error rather than a clean io.EOF.
func NewVerifyingReader(src io.ReadCloser, hash Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errNilHash
	}
	decoded, err := multihash.Decode(idBytes(hash))
	if err != nil {
		return nil, WrapDetail(errVerifyingReader, "expected hash not a multihash", err)
	}
	if decoded.Code != multihash.SHA2_256 {
		return nil, WithDetail(errVerifyingReader, "unsupported hash algorithm "+decoded.Name+" (only sha2-256)")
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
			return 0, WithDetail(errContent, vr.id+": file longer than expected "+strconv.FormatUint(vr.expectedSize, 10)+" bytes")
		}
		if probeErr != nil && probeErr != io.EOF {
			return 0, probeErr
		}
		if !bytes.Equal(vr.hasher.Sum(nil), vr.expectedDigest) {
			return 0, WithDetail(errContent, vr.id+": hash mismatch — bytes have been modified")
		}
		return n, io.EOF
	}
	if err == io.EOF {
		vr.done = true
		return n, WithDetail(errContent, vr.id+": truncated — got "+strconv.FormatUint(vr.read, 10)+" bytes, expected "+strconv.FormatUint(vr.expectedSize, 10))
	}
	return n, err
}

func (vr *verifyingReader) Close() error { return vr.src.Close() }
