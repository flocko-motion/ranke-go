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

// Sign and verify per paper §4.1. The "self-describing" requirement
// is satisfied by multikey-style framing: pubkey and signature bytes
// both carry a leading multicodec varint naming the scheme.
//
// Pubkey format:  <multicodec varint><raw public key bytes>
// Signature/id:   <multicodec varint><raw signature bytes>
//
// The same multicodec code (e.g. ed25519-pub = 0xed) tags both the
// pubkey and the signature produced by the matching private key —
// the payload length tells them apart (32 bytes pubkey vs 64 bytes
// Ed25519 signature). For the identity Sign case (empty pubkey), id
// stays a plain multihash; the multicodec code at its head (e.g.
// sha2-256 = 0x12) tags it as a hash, not a signature.
//
// The reference implementation supports Ed25519 (RFC 8032) today.
// Other schemes can be added by allocating a new multicodec code and
// wiring sign/verify here.

// EncodePublicKey wraps a Go public key as a self-describing
// multikey: <multicodec varint><raw key bytes>. Returns an error for
// unsupported key types.
func EncodePublicKey(pub crypto.PublicKey) ([]byte, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return prependCode(multicodec.Ed25519Pub, k), nil
	default:
		return nil, withDetail(errEncodePubkey, reflect.TypeOf(pub).String())
	}
}

// DecodePublicKey parses a multikey-encoded public key. Returns the
// scheme code and the typed Go public key.
func DecodePublicKey(b []byte) (multicodec.Code, crypto.PublicKey, error) {
	code, rest, err := readCode(b)
	if err != nil {
		return 0, nil, wrap(errDecodePubkey, err)
	}
	switch code {
	case multicodec.Ed25519Pub:
		if len(rest) != ed25519.PublicKeySize {
			return code, nil, withDetail(errDecodePubkey, "ed25519 pubkey has "+strconv.Itoa(len(rest))+" bytes, want "+strconv.Itoa(ed25519.PublicKeySize))
		}
		return code, ed25519.PublicKey(rest), nil
	default:
		return code, nil, withDetail(errDecodePubkey, "unsupported multicodec "+code.String()+" (0x"+strconv.FormatUint(uint64(code), 16)+")")
	}
}

// signHash applies the supplied signer to the hash, returning the
// multikey-wrapped signature bytes (suitable for use as an id
// payload). Returns the hash unchanged when signingKey is nil — the
// identity Sign case from §4.1.
func signHash(signingKey crypto.Signer, hash []byte) ([]byte, error) {
	if signingKey == nil {
		return hash, nil
	}
	switch pub := signingKey.Public().(type) {
	case ed25519.PublicKey:
		// Ed25519: pass crypto.Hash(0) per stdlib contract; rand is
		// ignored because Ed25519 is deterministic.
		sig, err := signingKey.Sign(rand.Reader, hash, crypto.Hash(0))
		if err != nil {
			return nil, wrapDetail(errSignHash, "ed25519 sign", err)
		}
		return prependCode(multicodec.Ed25519Pub, sig), nil
	default:
		return nil, withDetail(errSignHash, "unsupported signer public key type "+reflect.TypeOf(pub).String())
	}
}

// verifySignature checks that idPayload is a valid signature by the
// owner of pubkey over hash. When pubkey is empty, idPayload is
// expected to equal hash (identity Sign).
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
		return wrapDetail(errVerifySig, "decode pubkey", err)
	}
	sigCode, sig, err := splitCode(idPayload)
	if err != nil {
		return wrapDetail(errVerifySig, "decode signature", err)
	}
	if pubCode != sigCode {
		return withDetail(errVerifySig, "scheme mismatch (pubkey="+pubCode.String()+", sig="+sigCode.String()+")")
	}
	switch pubCode {
	case multicodec.Ed25519Pub:
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return withDetail(errVerifySig, "ed25519 pubkey type "+reflect.TypeOf(pub).String())
		}
		if len(sig) != ed25519.SignatureSize {
			return withDetail(errVerifySig, "ed25519 sig has "+strconv.Itoa(len(sig))+" bytes, want "+strconv.Itoa(ed25519.SignatureSize))
		}
		if !ed25519.Verify(edPub, hash, sig) {
			return errEd25519Verify
		}
		return nil
	default:
		return withDetail(errVerifySig, "unsupported scheme "+pubCode.String())
	}
}

// Keypair pairs a private signing key with its multikey-encoded
// public key — the two pieces of key material a contributor claim
// needs. LoadPrivateKey returns one of these in a single call.
type Keypair struct {
	Private crypto.Signer
	Pubkey  []byte // multikey-encoded (see EncodePublicKey)
}

// LoadPrivateKey loads an Ed25519 PKCS#8 PEM private key from path
// and pre-computes its multikey-encoded public key. Returns the
// pair in a single value so callers don't repeat the load + encode
// dance.
func LoadPrivateKey(path string) (Keypair, error) {
	priv, err := LoadEd25519PrivateKeyPEM(path)
	if err != nil {
		return Keypair{}, err
	}
	pubkey, err := EncodePublicKey(priv.Public())
	if err != nil {
		return Keypair{}, wrapDetail(errLoadKeypair, "encode pubkey", err)
	}
	return Keypair{Private: priv, Pubkey: pubkey}, nil
}

// LoadEd25519PrivateKeyPEM loads an Ed25519 private key from a
// PKCS#8 PEM file (as produced by `openssl genpkey -algorithm
// ed25519`). The returned key satisfies crypto.Signer.
func LoadEd25519PrivateKeyPEM(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, wrapDetail(errLoadPrivKey, "read "+path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, withDetail(errLoadPrivKey, path+": no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, wrapDetail(errLoadPrivKey, path+": parse PKCS#8", err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, withDetail(errLoadPrivKey, path+": not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
	}
	return ed, nil
}

// LoadEd25519PublicKeyPEM loads an Ed25519 public key from a
// SubjectPublicKeyInfo PEM file (as produced by `openssl pkey
// -pubout`).
//
//deadcode:keep
func LoadEd25519PublicKeyPEM(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, wrapDetail(errLoadPubKey, "read "+path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, withDetail(errLoadPubKey, path+": no PEM block found")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, wrapDetail(errLoadPubKey, path+": parse SPKI", err)
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, withDetail(errLoadPubKey, path+": not an Ed25519 key (got "+reflect.TypeOf(key).String()+")")
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

// splitCode reads the leading varint code and returns it plus the
// remaining payload bytes.
func splitCode(b []byte) (multicodec.Code, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errInvalidVarint
	}
	return multicodec.Code(v), b[n:], nil
}

// readCode is an alias for splitCode used at parse sites where the
// semantics is clearer as "read the leading code, return the rest".
func readCode(b []byte) (multicodec.Code, []byte, error) {
	return splitCode(b)
}

// bytesEqual is a small helper to avoid importing "bytes" here.
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
