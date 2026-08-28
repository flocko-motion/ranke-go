// package: internal/vectors / check
// type:    logic
// job:     runs an artifact set's cases through the library, so the generator's self-check and the
// conformance gate ask the same question of the same code
// limits:  reports what the library did; deciding whether that is conformant is the caller's
package vectors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rankegraph/ranke-go"
)

var errCheck = errors.New("vectors.Check")

// Outcome is what the library made of one case, beside what the manifest expected.
type Outcome struct {
	File     string
	Reason   string
	Why      string
	Expected bool // the manifest's verify flag
	Accepted bool // what the library did
}

// Holds reports whether the library agreed with the manifest.
func (o Outcome) Holds() bool { return o.Expected == o.Accepted }

// CheckClaims runs every claim case in the set at root: the ones marked verify must
// pass the closure verifier, the rest must be refused. The universe holds exactly
// what the set calls valid, so a contributor resolves only when the set carries it.
func CheckClaims(ctx context.Context, root string, m *Manifest) ([]Outcome, error) {
	u := ranke.NewMemoryUniverse()
	for _, c := range m.Claims {
		if !c.Verify {
			continue
		}
		cl, err := Decode(root, c)
		if err != nil {
			return nil, err
		}
		if err := u.PutClaims(ctx, []ranke.Claim{cl}); err != nil {
			return nil, fmt.Errorf("%w: store %s: %w", errCheck, c.File, err)
		}
	}

	out := make([]Outcome, 0, len(m.Claims))
	for _, c := range m.Claims {
		out = append(out, Outcome{
			File:     c.File,
			Reason:   c.Reason,
			Why:      c.Why,
			Expected: c.Verify,
			Accepted: Accepts(ctx, u, root, c),
		})
	}
	return out, nil
}

// Accepts reports whether the library takes the case's bytes under the case's id,
// resolving its closure through u.
func Accepts(ctx context.Context, u ranke.Universe, root string, c ClaimCase) bool {
	cl, err := Decode(root, c)
	if err != nil {
		return false // a malformed id or record is refused before any verification
	}
	g, err := ranke.NewGraph(ctx, u)
	if err != nil {
		return false
	}
	if err := g.AddClaims(ctx, cl); err != nil {
		return false
	}
	run := g.Verify()
	run.Wait()
	return run.Err() == nil && len(run.Failures()) == 0
}

// Decode reads a case's record under the id it is offered as.
func Decode(root string, c ClaimCase) (ranke.Claim, error) {
	raw, err := os.ReadFile(filepath.Join(root, c.File))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCheck, err)
	}
	id, err := ranke.ParseId(c.Id)
	if err != nil {
		return nil, fmt.Errorf("%w: parse id of %s: %w", errCheck, c.File, err)
	}
	cl, err := ranke.DecodeClaim(id, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: decode %s: %w", errCheck, c.File, err)
	}
	return cl, nil
}

// CheckContent checks each blob against the hash it is filed under, which is the
// whole of content integrity (§5.10).
func CheckContent(root string, m *Manifest) ([]Outcome, error) {
	out := make([]Outcome, 0, len(m.Content))
	for _, c := range m.Content {
		blob, err := os.ReadFile(filepath.Join(root, c.File))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errCheck, err)
		}
		hash, err := ranke.ParseId(c.Hash)
		if err != nil {
			return nil, fmt.Errorf("%w: parse hash of %s: %w", errCheck, c.File, err)
		}
		out = append(out, Outcome{
			File:     c.File,
			Reason:   c.Reason,
			Why:      c.Why,
			Expected: c.Verify,
			Accepted: ranke.VerifyContent(hash, uint64(len(blob)), blob) == nil,
		})
	}
	return out, nil
}
