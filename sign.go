// package: ranke / sign
// type:    crypto
// job:     multikey-framed Sign/verify (§4.1) plus Ed25519 key encoding and PEM loading
// limits:  does not compute the hash being signed (-> hash); supports only Ed25519 today
package ranke

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"os"
	"reflect"
	"strconv"

	"github.com/multiformats/go-multicodec"
)

// Multikey framing (§4.1): pubkey and signature bytes each lead with a multicodec
// varint naming the scheme (`V-SIGN`), and the two schemes differ, so the code alone
// says which a byte string is.

// signatureCodeFor is the signature scheme a pubkey scheme signs with. Distinct codes
// are what keep the two apart; a shared one leaves only payload length to tell them by.
func signatureCodeFor(pubCode multicodec.Code) (multicodec.Code, bool) {
	if pubCode == multicodec.Ed25519Pub {
		return multicodec.Eddsa, true
	}
	return 0, false
}

// EncodePublicKey wraps a Go public key as a multikey:
// <multicodec varint><raw key bytes>.
func EncodePublicKey(pub crypto.PublicKey) ([]byte, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return prependCode(multicodec.Ed25519Pub, k), nil
	default:
		return nil, WithDetail(errEncodePubkey, reflect.TypeOf(pub).String())
	}
}

// DecodePublicKey parses a multikey into its scheme code and typed Go key.
func DecodePublicKey(b []byte) (multicodec.Code, crypto.PublicKey, error) {
	code, rest, err := readCode(b)
	if err != nil {
		return 0, nil, Wrap(errDecodePubkey, err)
	}
	switch code {
	case multicodec.Ed25519Pub:
		if len(rest) != ed25519.PublicKeySize {
			return code, nil, WithDetail(errDecodePubkey, "ed25519 pubkey has "+strconv.Itoa(len(rest))+" bytes, want "+strconv.Itoa(ed25519.PublicKeySize))
		}
		return code, ed25519.PublicKey(rest), nil
	default:
		return code, nil, WithDetail(errDecodePubkey, "unsupported multicodec "+code.String()+" (0x"+strconv.FormatUint(uint64(code), 16)+")")
	}
}

// signHash returns the multikey-wrapped signature over hash, or hash itself
// when signingKey is nil — the identity Sign case (§4.1).
func signHash(signingKey crypto.Signer, hash []byte) ([]byte, error) {
	if signingKey == nil {
		return hash, nil
	}
	switch pub := signingKey.Public().(type) {
	case ed25519.PublicKey:
		// crypto.Hash(0) per the stdlib Ed25519 signer contract.
		sig, err := signingKey.Sign(rand.Reader, hash, crypto.Hash(0))
		if err != nil {
			return nil, WrapDetail(errSignHash, "ed25519 sign", err)
		}
		return prependCode(multicodec.Eddsa, sig), nil
	default:
		return nil, WithDetail(errSignHash, "unsupported signer public key type "+reflect.TypeOf(pub).String())
	}
}

// verifySignature checks idPayload as a signature by pubkey's owner over hash;
// an empty pubkey means idPayload must equal hash (identity Sign).
func verifySignature(pubkey, hash, idPayload []byte) error {
	if len(pubkey) == 0 {
		// Identity Sign: id is just the hash.
		if !bytesEqual(hash, idPayload) {
			return errIdentitySignMismatch
		}
		return nil
	}
	pubCode, pub, err := DecodePublicKey(pubkey)
	if err != nil {
		return WrapDetail(errVerifySig, "decode pubkey", err)
	}
	sigCode, sig, err := splitCode(idPayload)
	if err != nil {
		return WrapDetail(errVerifySig, "decode signature", err)
	}
	want, paired := signatureCodeFor(pubCode)
	if !paired {
		return WithDetail(errVerifySig, "unsupported scheme "+pubCode.String())
	}
	if sigCode != want {
		return WithDetail(errVerifySig, "scheme mismatch (pubkey="+pubCode.String()+
			" signs with "+want.String()+", sig="+sigCode.String()+")")
	}
	switch pubCode {
	case multicodec.Ed25519Pub:
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return WithDetail(errVerifySig, "ed25519 pubkey type "+reflect.TypeOf(pub).String())
		}
		if len(sig) != ed25519.SignatureSize {
			return WithDetail(errVerifySig, "ed25519 sig has "+strconv.Itoa(len(sig))+" bytes, want "+strconv.Itoa(ed25519.SignatureSize))
		}
		if !ed25519.Verify(edPub, hash, sig) {
			return errEd25519Verify
		}
		return nil
	default:
		return WithDetail(errVerifySig, "unsupported scheme "+pubCode.String())
	}
}

// Keypair pairs a private signing key with its multikey-encoded public key,
// the key material a contributor claim needs.
type Keypair struct {
	Private crypto.Signer
	Pubkey  []byte // multikey-encoded (see EncodePublicKey)
}

// LoadPrivateKey loads an Ed25519 PKCS#8 PEM private key from path and
// pre-computes its multikey-encoded public key.
func LoadPrivateKey(path string) (Keypair, error) {
	priv, err := LoadEd25519PrivateKeyPEM(path)
	if err != nil {
		return Keypair{}, err
	}
	pubkey, err := EncodePublicKey(priv.Public())
	if err != nil {
		return Keypair{}, WrapDetail(errLoadKeypair, "encode pubkey", err)
	}
	return Keypair{Private: priv, Pubkey: pubkey}, nil
}

// LoadEd25519PrivateKeyPEM loads an Ed25519 private key from a PKCS#8 PEM
// file (`openssl genpkey -algorithm ed25519`).
func LoadEd25519PrivateKeyPEM(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, WrapDetail(errLoadPrivKey, "read "+path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, WithDetail(errLoadPrivKey, path+": no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, WrapDetail(errLoadPrivKey, path+": parse PKCS#8", err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, WithDetail(errLoadPrivKey, path+": not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
	}
	return ed, nil
}

// LoadEd25519PublicKeyPEM loads an Ed25519 public key from a
// SubjectPublicKeyInfo PEM file (`openssl pkey -pubout`).
func LoadEd25519PublicKeyPEM(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, WrapDetail(errLoadPubKey, "read "+path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, WithDetail(errLoadPubKey, path+": no PEM block found")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, WrapDetail(errLoadPubKey, path+": parse SPKI", err)
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, WithDetail(errLoadPubKey, path+": not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
	}
	return ed, nil
}

// --- multikey/multicodec varint helpers ---

// prependCode emits <varint code><payload>.
func prependCode(code multicodec.Code, payload []byte) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(code))
	out := make([]byte, 0, n+len(payload))
	out = append(out, buf[:n]...)
	out = append(out, payload...)
	return out
}

// splitCode reads the leading varint code and returns it with the payload.
func splitCode(b []byte) (multicodec.Code, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errInvalidVarint
	}
	return multicodec.Code(v), b[n:], nil
}

// readCode reads the leading code and returns the rest.
func readCode(b []byte) (multicodec.Code, []byte, error) {
	return splitCode(b)
}

// bytesEqual compares two byte slices, keeping "bytes" out of the imports.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
