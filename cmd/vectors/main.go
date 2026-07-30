// package: main / vectors
// type:    cmd
// job:     generates the cross-implementation reference artifacts — a toy graph of claim records
// plus records that must be rejected, described by a manifest
// limits:  renders bytes only; the artifacts are the spec's, and which of them are correct is
// settled against the paper, not against this program
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// manifest names every artifact and the outcome an implementation must reach for it.
type manifest struct {
	Note       string        `json:"note"`
	Provenance provenance    `json:"provenance"`
	Claims     []claimCase   `json:"claims"`
	Content    []contentCase `json:"content"`
}

// provenance records what produced the set, so a reader can reproduce it. Version
// is "(devel)" unless the generator ran as a released module.
type provenance struct {
	Generator   string `json:"generator"`
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
}

// claimCase is one claim record under the id it is offered as. The id is not part
// of the record, so the pairing is what a case asserts.
type claimCase struct {
	File   string `json:"file"`
	Id     string `json:"id"`
	Verify bool   `json:"verify"`
	Reason string `json:"reason"`
	Why    string `json:"why"`
}

// contentCase is one content blob under the hash it is offered as.
type contentCase struct {
	File   string `json:"file"`
	Hash   string `json:"hash"`
	Verify bool   `json:"verify"`
	Reason string `json:"reason"`
	Why    string `json:"why"`
}

// Reason codes, so a test asserts the rejection it expected rather than any.
const (
	reasonOK              = "ok"
	reasonIDMismatch      = "id_mismatch"
	reasonWrongMessage    = "wrong_message"
	reasonMalformedID     = "malformed_id"
	reasonIdentitySign    = "identity_sign_mismatch"
	reasonNoContributor   = "unresolvable_contributor"
	reasonHeightWrong     = "height_wrong"
	reasonContentMismatch = "content_hash_mismatch"
)

// epoch fixes every timestamp, so a regenerated set is byte-identical.
var epoch = time.Unix(1700000000, 0).UTC()

// generatorPath names this tool in the manifest.
const generatorPath = "github.com/flocko-motion/ranke-go/cmd/vectors"

func main() {
	out := flag.String("out", "", "directory to write the artifacts into (required)")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	p := provenance{
		Generator:   generatorPath,
		Version:     generatorVersion(),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := run(context.Background(), *out, p); err != nil {
		fmt.Fprintln(os.Stderr, "vectors:", err)
		os.Exit(1)
	}
}

// generatorVersion reads the module version this ran as, so the manifest names a
// release rather than a working copy.
func generatorVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" {
		return "unknown"
	}
	return bi.Main.Version
}

// run builds the artifacts and writes them under dir.
func run(ctx context.Context, dir string, p provenance) error {
	g := &gen{dir: dir, prov: p, ids: map[string]ranke.Id{}, raw: map[string][]byte{}}
	for _, sub := range []string{"claims", "content"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := g.toyGraph(ctx); err != nil {
		return fmt.Errorf("toy graph: %w", err)
	}
	if err := g.broken(ctx); err != nil {
		return fmt.Errorf("broken records: %w", err)
	}
	return g.writeManifest()
}

// gen accumulates artifacts and the cases describing them.
type gen struct {
	dir     string
	prov    provenance
	claims  []claimCase
	content []contentCase
	ids     map[string]ranke.Id
	raw     map[string][]byte
	who     ranke.Contributor // the root identity, which the rejected cases reuse
}

// writeManifest renders the manifest last, when every case is known.
func (g *gen) writeManifest() error {
	m := manifest{
		Note: "Reference artifacts for the Ranke-Graph ADT. A claim record is {1: S(node)}; " +
			"its id is not in the record, so each case pairs bytes with the id they are offered " +
			"under. Verification hashes the record as received, never a re-encode.",
		Provenance: g.prov,
		Claims:     g.claims,
		Content:    g.content,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(g.dir, "manifest.json"), append(b, '\n'), 0o644)
}

// addClaim writes a claim that must verify, remembering it for the broken cases.
func (g *gen) addClaim(name string, c ranke.Claim, why string) error {
	raw, err := c.EncodeCBOR(ranke.FormOriginal)
	if err != nil {
		return err
	}
	file := filepath.Join("claims", name+".cbor")
	if err := os.WriteFile(filepath.Join(g.dir, file), raw, 0o644); err != nil {
		return err
	}
	g.ids[name], g.raw[name] = c.ID(), raw
	g.claims = append(g.claims, claimCase{
		File: file, Id: c.ID().String(), Verify: true, Reason: reasonOK, Why: why,
	})
	return nil
}

// addBroken writes a record that must be rejected under the id it names.
func (g *gen) addBroken(name string, raw []byte, id, reason, why string) error {
	file := filepath.Join("claims", name+".cbor")
	if err := os.WriteFile(filepath.Join(g.dir, file), raw, 0o644); err != nil {
		return err
	}
	g.claims = append(g.claims, claimCase{
		File: file, Id: id, Verify: false, Reason: reason, Why: why,
	})
	return nil
}

// addContent writes a blob under the hash it is offered as.
func (g *gen) addContent(name string, blob []byte, hash ranke.Id, ok bool, reason, why string) error {
	file := filepath.Join("content", name+".bin")
	if err := os.WriteFile(filepath.Join(g.dir, file), blob, 0o644); err != nil {
		return err
	}
	g.content = append(g.content, contentCase{
		File: file, Hash: hash.String(), Verify: ok, Reason: reason, Why: why,
	})
	return nil
}

// signer derives a fixed Ed25519 key from seed, so the artifacts reproduce.
func signer(seed string) ed25519.PrivateKey {
	h := sha256.Sum256([]byte(seed))
	return ed25519.NewKeyFromSeed(h[:])
}

// contributorClaim builds a root contributor: an initial node whose content is its
// own multikey pubkey (§5.7), signed by the key it publishes.
func contributorClaim(ctx context.Context, priv ed25519.PrivateKey, at time.Time) (ranke.Claim, ranke.Contributor, error) {
	pub, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, nil, err
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, nil, err
	}
	who, err := c.AsContributor(ctx, nil, priv)
	if err != nil {
		return nil, nil, err
	}
	return c, who, nil
}
