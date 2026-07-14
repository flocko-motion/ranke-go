package ranke

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for content integrity (§5.10) — storage-agnostic:
// content is addressed by the multihash of its bytes, so verification is a
// property of the bytes, not of where they live. VerifyContent checks a
// buffered blob; NewVerifyingReader checks a stream, holding back its final
// block so a truncated / over-long / modified stream fails on the last Read
// instead of ending in a clean io.EOF. This is the machinery behind D5.

// --- VerifyContent (buffered) ------------------------------------------

// TestVerifyContentAccepts: bytes that match their hash and size pass.
func TestVerifyContentAccepts(t *testing.T) {
	data := []byte("the exact content bytes")
	hash, err := HashContent(data)
	require.NoError(t, err)
	require.NoError(t, VerifyContent(hash, uint64(len(data)), data))
}

// TestVerifyContentSizeMismatch: the right bytes with the wrong declared
// size are rejected as an integrity failure — cheap truncation/extension
// detection without rehashing.
func TestVerifyContentSizeMismatch(t *testing.T) {
	data := []byte("content")
	hash, err := HashContent(data)
	require.NoError(t, err)
	err = VerifyContent(hash, uint64(len(data))+1, data)
	require.ErrorIs(t, err, ErrIntegrity)
}

// TestVerifyContentDigestMismatch: bytes of the right length but wrong
// content are rejected — the hash, not the length, is the real guard.
func TestVerifyContentDigestMismatch(t *testing.T) {
	hash, err := HashContent([]byte("hello"))
	require.NoError(t, err)
	err = VerifyContent(hash, 5, []byte("hellO")) // same length, one byte off
	require.ErrorIs(t, err, ErrIntegrity)
}

// TestVerifyContentNilHash: a nil expected hash is a usage error, not a
// pass.
func TestVerifyContentNilHash(t *testing.T) {
	require.Error(t, VerifyContent(nil, 0, nil))
}

// --- NewVerifyingReader (streamed) -------------------------------------

func verifyingReaderOver(t *testing.T, streamed []byte, hash Id, declaredSize uint64) io.ReadCloser {
	t.Helper()
	src := io.NopCloser(bytes.NewReader(streamed))
	vr, err := NewVerifyingReader(src, hash, declaredSize)
	require.NoError(t, err)
	return vr
}

// TestVerifyingReaderCleanStream: an honest stream reads through to EOF
// and yields exactly the content, no error.
func TestVerifyingReaderCleanStream(t *testing.T) {
	data := []byte("streamed content, verified on the way through")
	hash, err := HashContent(data)
	require.NoError(t, err)

	vr := verifyingReaderOver(t, data, hash, uint64(len(data)))
	got, err := io.ReadAll(vr)
	require.NoError(t, err, "honest stream verifies to a clean EOF")
	require.Equal(t, data, got)
}

// TestVerifyingReaderDetectsTruncation: a stream shorter than the declared
// size fails rather than ending cleanly.
func TestVerifyingReaderDetectsTruncation(t *testing.T) {
	data := []byte("hello")
	hash, err := HashContent(data)
	require.NoError(t, err)

	// Declare more bytes than the source actually holds.
	vr := verifyingReaderOver(t, data, hash, uint64(len(data))+5)
	_, err = io.ReadAll(vr)
	require.Error(t, err, "a truncated stream must not verify")
}

// TestVerifyingReaderDetectsOverlong: a stream longer than the declared
// size is caught by the overflow probe.
func TestVerifyingReaderDetectsOverlong(t *testing.T) {
	full := []byte("hello world")
	// Declare fewer bytes than the source holds; the hash is over the
	// declared-length prefix, so only the length is wrong.
	prefix := full[:5]
	hash, err := HashContent(prefix)
	require.NoError(t, err)

	vr := verifyingReaderOver(t, full, hash, uint64(len(prefix)))
	_, err = io.ReadAll(vr)
	require.Error(t, err, "an over-long stream must not verify")
}

// TestVerifyingReaderDetectsTamper: a stream of the right length but wrong
// bytes fails the digest check on the final read.
func TestVerifyingReaderDetectsTamper(t *testing.T) {
	hash, err := HashContent([]byte("hello"))
	require.NoError(t, err)

	// Same length, different bytes than the hash addresses.
	vr := verifyingReaderOver(t, []byte("hellO"), hash, 5)
	_, err = io.ReadAll(vr)
	require.Error(t, err, "modified bytes must fail the digest check")
}

// TestVerifyingReaderNilHash: constructing over a nil hash is a usage
// error.
func TestVerifyingReaderNilHash(t *testing.T) {
	_, err := NewVerifyingReader(io.NopCloser(bytes.NewReader(nil)), nil, 0)
	require.Error(t, err)
}
