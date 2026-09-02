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
	u, err := validUniverse(ctx, root, m)
	if err != nil {
		return nil, err
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

// validUniverse holds exactly the claims the set calls valid — the closure every
// other case is resolved against.
func validUniverse(ctx context.Context, root string, m *Manifest) (ranke.Universe, error) {
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
	return u, nil
}

// CheckBookmarks runs every bookmark case in the set at root. A standalone case is
// judged as one record — its shape, its signature, its slot and its k (`V-BMENV`,
// `V-BMSIG`, `V-BMSLOT`, `V-BMREF`). A case naming a list is judged with the rest of
// that list: assembled into a 𝒰_hist, opened at the entry marked Open, then verified
// whole, which is what reaches `V-BMGAPLESS`.
func CheckBookmarks(ctx context.Context, root string, m *Manifest) ([]Outcome, error) {
	u, err := validUniverse(ctx, root, m)
	if err != nil {
		return nil, err
	}
	lists, err := checkLists(ctx, root, m, u)
	if err != nil {
		return nil, err
	}
	out := make([]Outcome, 0, len(m.Bookmarks))
	for _, c := range m.Bookmarks {
		accepted, ok := lists[c.List]
		if !ok {
			if accepted, err = acceptsBookmark(ctx, u, root, c); err != nil {
				return nil, err
			}
		}
		out = append(out, Outcome{
			File: c.File, Reason: c.Reason, Why: c.Why, Expected: c.Verify, Accepted: accepted,
		})
	}
	return out, nil
}

// checkLists opens and verifies each named list once, so every case in it reports the
// list's own outcome rather than its record's.
func checkLists(ctx context.Context, root string, m *Manifest, u ranke.Universe) (map[string]bool, error) {
	names := map[string]bool{}
	for _, c := range m.Bookmarks {
		if c.List != "" {
			names[c.List] = true
		}
	}
	out := make(map[string]bool, len(names))
	for name := range names {
		marks, err := OpenList(ctx, root, m, name, u)
		out[name] = err == nil && marks.Verify(ctx) == nil
	}
	return out, nil
}

// OpenList assembles every case naming list into a 𝒰_hist and opens the list at the
// entry marked Open — the way a reader holding one bookmark id reaches it (§Backup).
func OpenList(ctx context.Context, root string, m *Manifest, list string, u ranke.Universe) (*ranke.Bookmarks, error) {
	store := ranke.NewMemoryBookmarks()
	var open ranke.Id
	for _, c := range m.Bookmarks {
		if c.List != list {
			continue
		}
		raw, slot, err := readBookmark(root, c)
		if err != nil {
			return nil, err
		}
		if err := store.Put(ctx, slot, raw); err != nil {
			return nil, fmt.Errorf("%w: store %s: %w", errCheck, c.File, err)
		}
		if c.Open {
			open = slot
		}
	}
	if open == nil {
		return nil, fmt.Errorf("%w: bookmark list %q names no entry to open it from", errCheck, list)
	}
	return ranke.OpenBookmarks(ctx, store, u, open)
}

// acceptsBookmark reports whether the library takes one record as the bookmark its
// slot claims to hold.
func acceptsBookmark(ctx context.Context, u ranke.Universe, root string, c BookmarkCase) (bool, error) {
	raw, slot, err := readBookmark(root, c)
	if err != nil {
		return false, err
	}
	bm, err := ranke.VerifyBookmark(ctx, u, slot, raw)
	if err != nil {
		return false, nil
	}
	return ranke.CheckBookmarkHead(ctx, u, bm) == nil, nil
}

// readBookmark reads a bookmark case's bytes and the slot it is offered at.
func readBookmark(root string, c BookmarkCase) ([]byte, ranke.Id, error) {
	raw, err := os.ReadFile(filepath.Join(root, c.File))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errCheck, err)
	}
	slot, err := ranke.ParseId(c.Slot)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: parse slot of %s: %w", errCheck, c.File, err)
	}
	return raw, slot, nil
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
