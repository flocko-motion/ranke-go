package ranke

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/multiformats/go-multicodec"
	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the signing primitives (§4.1, §5.7) exercised
// DIRECTLY — no Graph, no claim. (The end-to-end signing story through a
// Graph lives a layer up in tests/sign_test.go.) These pin: multikey
// public-key encoding round-trips and rejects the unsupported; sign then
// verify accepts, and tampering / wrong key / identity-Sign behave per
// spec. Authenticity (D3) and verifiability (D5) reduce to these.

func ed25519Keys(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	return priv, pubkey
}

// --- public-key encoding ------------------------------------------------

// TestEncodeDecodePublicKeyRoundTrip: a multikey-encoded pubkey decodes
// back to the same Ed25519 key, naming its scheme (self-describing, §4.1).
func TestEncodeDecodePublicKeyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = priv

	encoded, err := EncodePublicKey(pub)
	require.NoError(t, err)

	code, decoded, err := DecodePublicKey(encoded)
	require.NoError(t, err)
	require.Equal(t, multicodec.Ed25519Pub, code, "scheme is named in the encoding")
	edPub, ok := decoded.(ed25519.PublicKey)
	require.True(t, ok, "decodes to an ed25519 public key")
	require.Equal(t, pub, edPub, "the key bytes survive the round-trip")
}

// TestEncodePublicKeyRejectsUnsupported: a key type the scheme table
// doesn't know is refused, not silently mis-encoded.
func TestEncodePublicKeyRejectsUnsupported(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_, err = EncodePublicKey(ec.Public())
	require.Error(t, err, "ECDSA is not a supported signing scheme")
}

// TestDecodePublicKeyRejectsWrongLength: a well-framed ed25519 multikey
// with the wrong number of key bytes is rejected.
func TestDecodePublicKeyRejectsWrongLength(t *testing.T) {
	_, pubkey := ed25519Keys(t)
	truncated := pubkey[:len(pubkey)-1] // drop one key byte, keep the varint
	_, _, err := DecodePublicKey(truncated)
	require.Error(t, err)
}

// --- sign / verify ------------------------------------------------------

// TestSignVerifyRoundTrip: a signature by a key verifies against that
// key's pubkey over the same hash.
func TestSignVerifyRoundTrip(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	hash, err := hashContent([]byte("the record bytes"))
	require.NoError(t, err)

	sig, err := signHash(priv, hash.raw)
	require.NoError(t, err)
	require.NoError(t, verifySignature(pubkey, hash.raw, sig),
		"honest signature must verify")
}

// TestVerifyRejectsTamperedHash: a signature over hash H does not verify
// against a different hash H' — the §5.10 tamper-evidence at the crypto
// layer.
func TestVerifyRejectsTamperedHash(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	h1, _ := hashContent([]byte("original"))
	h2, _ := hashContent([]byte("modified"))

	sig, err := signHash(priv, h1.raw)
	require.NoError(t, err)
	require.Error(t, verifySignature(pubkey, h2.raw, sig),
		"signature over a different hash must not verify")
}

// TestVerifyRejectsWrongKey: a signature by key A does not verify against
// key B's pubkey — Bob cannot pass off a claim as Alice's (§5.7).
func TestVerifyRejectsWrongKey(t *testing.T) {
	alicePriv, _ := ed25519Keys(t)
	_, bobPubkey := ed25519Keys(t)
	hash, _ := hashContent([]byte("attributed to alice"))

	sig, err := signHash(alicePriv, hash.raw)
	require.NoError(t, err)
	require.Error(t, verifySignature(bobPubkey, hash.raw, sig),
		"a signature must not verify against a different key")
}

// TestIdentitySign: with no key and an empty pubkey, id IS the hash and
// verification reduces to byte equality (§5.7 identity-Sign).
func TestIdentitySign(t *testing.T) {
	hash, _ := hashContent([]byte("unsigned claim"))

	idPayload, err := signHash(nil, hash.raw)
	require.NoError(t, err)
	require.Equal(t, hash.raw, idPayload, "identity-Sign leaves the hash unchanged")

	require.NoError(t, verifySignature(nil, hash.raw, idPayload),
		"identity-Sign verifies when id equals the hash")
	require.Error(t, verifySignature(nil, hash.raw, []byte("something else")),
		"identity-Sign fails when id does not equal the hash")
}

// TestVerifySchemeMismatch: a pubkey and an id payload naming different
// schemes are rejected before any curve math runs.
func TestVerifySchemeMismatch(t *testing.T) {
	_, pubkey := ed25519Keys(t)
	hash, _ := hashContent([]byte("x"))

	// An id payload framed with a non-ed25519 multicodec prefix.
	var wrongScheme []byte
	wrongScheme = appendUvarint(wrongScheme, uint64(multicodec.Sha2_256))
	wrongScheme = append(wrongScheme, make([]byte, 64)...)

	require.Error(t, verifySignature(pubkey, hash.raw, wrongScheme),
		"pubkey scheme and signature scheme must match")
}

// appendUvarint is a tiny local varint writer so the test doesn't reach
// for an internal helper.
func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// --- PEM key loading ----------------------------------------------------

// TestLoadEd25519PEM: the PEM loaders round-trip a key from disk —
// LoadEd25519PrivateKeyPEM (PKCS#8), LoadPrivateKey (also precomputes the
// multikey pubkey), and LoadEd25519PublicKeyPEM (SPKI).
func TestLoadEd25519PEM(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(privPath,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))

	loaded, err := LoadEd25519PrivateKeyPEM(privPath)
	require.NoError(t, err)
	require.Equal(t, priv, loaded, "private key round-trips through PEM")

	kp, err := LoadPrivateKey(privPath)
	require.NoError(t, err)
	wantPub, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	require.Equal(t, wantPub, kp.Pubkey, "LoadPrivateKey precomputes the multikey pubkey")

	pubDer, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pubPath := filepath.Join(dir, "pub.pem")
	require.NoError(t, os.WriteFile(pubPath,
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer}), 0o600))
	loadedPub, err := LoadEd25519PublicKeyPEM(pubPath)
	require.NoError(t, err)
	require.Equal(t, pub, loadedPub, "public key round-trips through PEM")
}

// TestLoadEd25519PEMErrors: a missing file and a non-PEM file are rejected.
func TestLoadEd25519PEMErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadEd25519PrivateKeyPEM(filepath.Join(dir, "nope.pem"))
	require.Error(t, err, "missing file")

	bad := filepath.Join(dir, "bad.pem")
	require.NoError(t, os.WriteFile(bad, []byte("not a pem block"), 0o600))
	_, err = LoadEd25519PrivateKeyPEM(bad)
	require.Error(t, err, "not a PEM block (private)")
	_, err = LoadEd25519PublicKeyPEM(bad)
	require.Error(t, err, "not a PEM block (public)")
	_, err = LoadPrivateKey(bad)
	require.Error(t, err, "LoadPrivateKey propagates the load error")
}
