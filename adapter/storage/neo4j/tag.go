// package: neo4j / persistence-cache
// type:    adapter
// job:     native branch tagging — stamp each branch's closure with its _b_<branch> membership in
// ONE Cypher traversal per branch-revision, instead of the generic claim-by-claim walk
// limits:  the spine itself (a claim per revision) is read normally; only the closure stamping is
// lowered (-> universe_default_tagger.go for the k/v fallback)
package neo4j

import (
	"context"
	"strconv"

	"github.com/flocko-motion/ranke-go"
)

// cypherTagClosure stamps every claim reachable from a branch head that does not
// already carry the tag — one traversal, no per-claim round trip. Only claims are
// stamped (a labelless node is a bare reference stub), and "does not already
// carry it" is what makes the oldest revision to reach a claim win.
const cypherTagClosure = `
MATCH (h {id: $head})-[*0..]->(n)
WITH DISTINCT n
WHERE size(labels(n)) > 0 AND NOT $bkey IN keys(n)
SET n += $props`

// cypherTagSpine marks one branch-table claim with the revision it sits at.
const cypherTagSpine = `
MATCH (bt {id: $id})
SET bt += $props`

// Tag brings branch membership up to date for the archive at head. It reads the
// untagged part of the branch-table spine (one claim per revision, cheap), then
// lowers each revision's per-branch closure stamp to a single Cypher traversal.
func (u *neo4jUniverse) Tag(ctx context.Context, head ranke.Id) error {
	if head == nil {
		return nil
	}
	spine, base, err := u.untaggedSpine(ctx, head)
	if err != nil || len(spine) == 0 {
		return err
	}
	// Oldest→newest, so the earliest revision to reach a claim is the one that
	// stamps it — matching the generic tagger's prune-where-present rule.
	revision := base
	for i := len(spine) - 1; i >= 0; i, revision = i-1, revision+1 {
		bt := spine[i]
		height := int64(bt.Node().Height())
		for _, e := range bt.Edges(ranke.EdgeFilterType{Type: ranke.EdgeTypeBranch}) {
			name, err := e.GetField(ranke.FieldName)
			if err != nil {
				return err
			}
			bkey := ranke.BranchTagKey(name)
			if _, err := u.query(ctx, cypherTagClosure, map[string]any{
				"head":  e.Reference().String(),
				"bkey":  bkey,
				"props": map[string]any{bkey: height},
			}); err != nil {
				return ranke.WrapDetail(errQuery, "tag branch closure "+name, err)
			}
		}
		if _, err := u.query(ctx, cypherTagSpine, map[string]any{
			"id":    bt.ID().String(),
			"props": map[string]any{ranke.SpineRevKey: int64(revision)},
		}); err != nil {
			return ranke.WrapDetail(errQuery, "tag spine revision "+strconv.FormatUint(revision, 10), err)
		}
	}
	return nil
}

// untaggedSpine returns the branch-table claims from head back to the newest one
// already tagged (newest first), plus the revision number the walk resumes at.
func (u *neo4jUniverse) untaggedSpine(ctx context.Context, head ranke.Id) ([]ranke.Claim, uint64, error) {
	var spine []ranke.Claim
	base := uint64(0)
	for id := head; id != nil; {
		bt, err := ranke.GetClaim(ctx, u, id)
		if err != nil {
			return nil, 0, err
		}
		if r := bt.Tag(ranke.SpineRevKey); r != "" {
			k, err := strconv.ParseUint(r, 10, 64)
			if err != nil {
				return nil, 0, err
			}
			base = k + 1
			break
		}
		spine = append(spine, bt)
		id = nil
		for _, e := range bt.Edges(ranke.EdgeFilterTypes{Types: []string{ranke.EdgeTypeDiff, ranke.EdgeTypeBranches}}) {
			id = e.Reference()
			break
		}
	}
	return spine, base, nil
}
