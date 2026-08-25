package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
	cose "github.com/veraison/go-cose"
)

// TestEnvelopeHeadersArePinned: `V-ENV` fixes the headers to alg and nothing else,
// which is what keeps one claim to one stored form. The id hashes these bytes, so a
// spare header would mint a second id for the same claim, and both would verify.
func TestEnvelopeHeadersArePinned(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	payload := []byte("a serialized claim")

	honest, err := signEnvelope(priv, payload)
	require.NoError(t, err)
	require.NoError(t, verifyEnvelope(pubkey, honest), "the pinned shape verifies")

	// A signer of its own, so the variants are signed rather than merely malformed —
	// the point is that a well-signed envelope of another shape is still refused.
	signer, err := cose.NewSigner(cose.AlgorithmEd25519, priv)
	require.NoError(t, err)

	seal := func(t *testing.T, shape func(*cose.Sign1Message)) []byte {
		t.Helper()
		msg := cose.NewSign1Message()
		msg.Payload = payload
		msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmEd25519
		shape(msg)
		require.NoError(t, msg.Sign(nil, nil, signer))
		raw, err := msg.MarshalCBOR()
		require.NoError(t, err)
		return raw
	}

	for name, shape := range map[string]func(*cose.Sign1Message){
		"a spare protected parameter": func(m *cose.Sign1Message) {
			m.Headers.Protected[cose.HeaderLabelContentType] = "application/cbor"
		},
		"anything unprotected": func(m *cose.Sign1Message) {
			m.Headers.Unprotected[cose.HeaderLabelKeyID] = []byte("kid")
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := seal(t, shape)
			_, err := envelopePayload(raw)
			require.ErrorIs(t, err, ErrEnvelopeHeaders)
			require.ErrorIs(t, verifyEnvelope(pubkey, raw), ErrEnvelopeHeaders,
				"a signature over the wrong shape is not an answer")
		})
	}
}
