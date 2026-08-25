// package: ranke / codec_envelope
// type:    crypto
// job:     the claim envelope (`V-ENV`) — a COSE_Sign1 over S(v), which the Universe stores
// under id(v) = H(S(env(v)))
// limits:  the record it wraps is codec.go's, and this says nothing about its shape;
// resolves no contributor key (-> verify)
package ranke

import (
	"crypto"
	"crypto/ed25519"
	"reflect"

	cose "github.com/veraison/go-cose"
)

// The payload rides as bytes, so verification reads what is stored: re-encoding the
// map to recover what was signed would shift it as the alias taxonomy grows.

// signEnvelope returns S(env(v)): payload signed under signingKey, as a tagged
// COSE_Sign1. The id is the hash of these bytes (`V-ID`).
func signEnvelope(signingKey crypto.Signer, payload []byte) ([]byte, error) {
	if signingKey == nil {
		return nil, errEnvelopeNoKey
	}
	if _, ok := signingKey.Public().(ed25519.PublicKey); !ok {
		return nil, WithDetail(errSignEnvelope, "unsupported signer public key type "+reflect.TypeOf(signingKey.Public()).String())
	}
	signer, err := cose.NewSigner(cose.AlgorithmEd25519, signingKey)
	if err != nil {
		return nil, WrapDetail(errSignEnvelope, "signer", err)
	}
	msg := cose.NewSign1Message()
	msg.Payload = payload
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmEd25519
	if err := msg.Sign(nil, nil, signer); err != nil {
		return nil, WrapDetail(errSignEnvelope, "sign", err)
	}
	raw, err := msg.MarshalCBOR()
	if err != nil {
		return nil, WrapDetail(errSignEnvelope, "marshal", err)
	}
	return raw, nil
}

// envelopePayload returns the S(v) an envelope carries. Bytes of another shape fail
// here, which is how content is told from a claim.
func envelopePayload(raw []byte) ([]byte, error) {
	msg, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if len(msg.Payload) == 0 {
		return nil, Wrap(errDecodeEnvelope, errEnvelopeNoPayload)
	}
	return msg.Payload, nil
}

// verifyEnvelope checks the signature against a multikey pubkey (`V-SIG`). It covers
// the stored payload, so authorship holds for the bytes filed under the id.
func verifyEnvelope(pubkey, raw []byte) error {
	if len(pubkey) == 0 {
		return errEnvelopeNoPubkey
	}
	_, pub, err := DecodePublicKey(pubkey)
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "decode pubkey", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return WithDetail(errVerifyEnvelope, "ed25519 pubkey type "+reflect.TypeOf(pub).String())
	}
	msg, err := decodeEnvelope(raw)
	if err != nil {
		return err
	}
	verifier, err := cose.NewVerifier(cose.AlgorithmEd25519, edPub)
	if err != nil {
		return WrapDetail(errVerifyEnvelope, "verifier", err)
	}
	if err := msg.Verify(nil, verifier); err != nil {
		return WrapDetail(errVerifyEnvelope, "verify", err)
	}
	return nil
}

// decodeEnvelope parses the stored bytes as a COSE_Sign1. go-cose refuses an
// untagged message and an empty signature, so what parses here is a signed record.
func decodeEnvelope(raw []byte) (*cose.Sign1Message, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(raw); err != nil {
		return nil, Wrap(errDecodeEnvelope, err)
	}
	return &msg, nil
}
