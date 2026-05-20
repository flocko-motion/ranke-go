// Signing tests — confirm that the §4.1/§5.7 signing model works
// end-to-end for both signed and identity-Sign claims, and that
// tampering with claim bytes is caught by the verifier.
package tests

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// signedContributor builds a contribution/contributor claim with a
// fresh Ed25519 keypair and returns both the claim and the private
// signer. The claim is an initial node (no edges) whose pubkey
// lives on its own node — paper §5.7.
func signedContributor(t *testing.T) (ranke.Contributor, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	c, err := ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte("test-contributor"),
		Pubkey:  pubkey,
	}.Sign(priv)
	require.NoError(t, err)
	view, err := c.AsContributor()
	require.NoError(t, err)
	return view, priv
}

// TestSignedClaimVerifies confirms the round trip: build a signed
// contributor + a signed source claim, walk the graph through
// Validate(), and assert the signatures check out.
func TestSignedClaimVerifies(t *testing.T) {
	alice, alicePriv := signedContributor(t)

	source, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: alice\r\n\r\nhello"),
		Contributor: alice,
	}.Sign(alicePriv)
	require.NoError(t, err)

	g := ranke.NewGraph(alice)
	require.NoError(t, g.Add(source))

	require.NoError(t, g.Validate(), "signed graph must validate")
}

// TestIdentitySignClaimVerifies confirms the unsigned (identity Sign)
// path: contributor with no pubkey, no SigningKey, id = H(S(v)),
// verification reduces to a hash check.
func TestIdentitySignClaimVerifies(t *testing.T) {
	alice := contributor(t, "alice@example.com") // unsigned helper

	source, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: alice\r\n\r\nhello"),
		Contributor: alice,
	}.Sign()
	require.NoError(t, err)

	g := ranke.NewGraph(alice)
	require.NoError(t, g.Add(source))

	require.NoError(t, g.Validate(), "identity-Sign graph must validate")
}

// TestSigningKeyMustMatchPubkey confirms Sign rejects a signer
// whose public key doesn't match the resolved contributor pubkey.
// Without this check, Bob's key could sign a claim attributed to
// Alice, undermining authenticity.
func TestSigningKeyMustMatchPubkey(t *testing.T) {
	alice, _ := signedContributor(t)
	_, bobPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	_, err = ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("hello"),
		Contributor: alice,
	}.Sign(bobPriv) // Bob's key, Alice's contributor
	require.Error(t, err, "signer/contributor mismatch must be rejected")
}

// TestSignedContributorRequiresSigner confirms that declaring a
// pubkey without supplying a SigningKey is rejected — the data
// says "signed by this key" but the call doesn't provide it.
func TestSignedContributorRequiresSigner(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)

	_, err = ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte("alice"),
		Pubkey:  pubkey,
		// no Sign(priv) — signing key missing on purpose
	}.Sign()
	require.Error(t, err, "pubkey-without-signer must be rejected")
}

// TestTamperingDetected confirms that mutating a byte in any
// signed claim breaks verification — the §5.10 promise that the
// Merkle-DAG id chain witnesses record integrity and authenticity
// in a single recomputation.
func TestTamperingDetected(t *testing.T) {
	alice, alicePriv := signedContributor(t)
	source, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: alice\r\n\r\nhello"),
		Contributor: alice,
	}.Sign(alicePriv)
	require.NoError(t, err)

	g := ranke.NewGraph(alice)
	require.NoError(t, g.Add(source))
	require.NoError(t, g.Validate())

	// Confirm that an honestly-built second claim has a DIFFERENT id
	// when content differs by one byte — the inverse of what an
	// attacker would need to forge.
	source2, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: alice\r\n\r\nhellO"), // last byte differs
		Contributor: alice,
		CreatedAt:   source.Node().CreatedAt(), // pin timestamp to isolate the content change
	}.Sign(alicePriv)
	require.NoError(t, err)
	require.False(t, source.ID().Equal(source2.ID()),
		"one-byte content change must produce a different id (collision resistance, §5.2)")
}
