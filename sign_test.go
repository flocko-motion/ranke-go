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

// Foundation unit tests for the signing primitives exercised DIRECTLY — no Graph, no
// claim. (The end-to-end story lives a layer up in tests/sign_test.go.) These pin
// multikey pubkey encoding, and the envelope: seal then verify accepts, while
// tampering, a wrong key and a missing key are each refused. Authenticity (D3) and
// verifiability (D5) reduce to these.

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

// TestSignVerifyRoundTrip: an envelope sealed under a key verifies against that key's
// pubkey.
func TestSignVerifyRoundTrip(t *testing.T) {
	priv, pubkey := ed25519Keys(t)

	env, err := signEnvelope(priv, []byte("the record bytes"))
	require.NoError(t, err)
	require.NoError(t, verifyEnvelope(pubkey, env), "an honest envelope must verify")
}

// TestVerifyRejectsTamperedPayload: the signature covers the payload, so swapping it
// inside an otherwise intact envelope is caught.
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	priv, pubkey := ed25519Keys(t)

	env, err := signEnvelope(priv, []byte("original"))
	require.NoError(t, err)

	msg, err := decodeEnvelope(env)
	require.NoError(t, err)
	msg.Payload = []byte("modified")
	tampered, err := msg.MarshalCBOR()
	require.NoError(t, err)

	require.Error(t, verifyEnvelope(pubkey, tampered),
		"a signature over different bytes must not verify")
}

// TestVerifyRejectsWrongKey: a signature by key A does not verify against key B's
// pubkey — Bob cannot pass off a claim as Alice's (`V-SIG`).
func TestVerifyRejectsWrongKey(t *testing.T) {
	alicePriv, _ := ed25519Keys(t)
	_, bobPubkey := ed25519Keys(t)

	env, err := signEnvelope(alicePriv, []byte("attributed to alice"))
	require.NoError(t, err)
	require.Error(t, verifyEnvelope(bobPubkey, env),
		"an envelope must not verify against a different key")
}

// TestEnvelopeNeedsKeyAndPubkey: every claim is signed (`V-SIG`), so sealing without a
// key and verifying without a pubkey are both refused rather than waved through.
func TestEnvelopeNeedsKeyAndPubkey(t *testing.T) {
	priv, _ := ed25519Keys(t)

	_, err := signEnvelope(nil, []byte("unsigned claim"))
	require.ErrorIs(t, err, errEnvelopeNoKey, "a claim cannot be sealed without a key")

	env, err := signEnvelope(priv, []byte("a signed claim"))
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelope(nil, env), errEnvelopeNoPubkey,
		"a contributor with no pubkey answers for nothing")
}

// TestVerifyRejectsForeignScheme: a pubkey framed under another multicodec is refused
// before any curve math runs (`V-SIGN`).
func TestVerifyRejectsForeignScheme(t *testing.T) {
	priv, _ := ed25519Keys(t)
	env, err := signEnvelope(priv, []byte("x"))
	require.NoError(t, err)

	foreign := prependCode(multicodec.Sha2_256, make([]byte, ed25519.PublicKeySize))
	require.Error(t, verifyEnvelope(foreign, env),
		"only the schemes `V-SIGN` names may answer for a signature")
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

// TestEveryIdIsAMultihash: with the signature moved into the envelope, one framing
// serves every id — a claim's, an edge's, and a content address (`V-ID`, `V-HASH`).
func TestEveryIdIsAMultihash(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("a note")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	hash, err := HashContent([]byte("some bytes"))
	require.NoError(t, err)

	require.Equal(t, "sha2-256", c.ID().Algorithm(), "a claim id hashes its envelope")
	require.Equal(t, "sha2-256", hash.Algorithm(), "a content address hashes its bytes")
	for _, e := range c.Edges() {
		require.Equal(t, "sha2-256", e.ID().Algorithm(), "an edge id hashes its record")
	}
}
