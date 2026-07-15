// package: ranke / tagger
// type:    logic
// job:     TagArchive — descend an archive's branch-table spine and stamp every branch's closure with per-claim entry revisions (a pure-functional runtime overlay), producing the head-id history it walks
// limits:  optional (gated by Capabilities.Tags); tags are set through the Universe's bulk GetClaimTags/SetClaimsTags — this file holds no backend I/O of its own
package ranke

import (
	"context"
	"strconv"
)

// The tagger owns these tag keys; the Universe just stores them. spineRevKey
// records the revision a branch-table claim sits at on the spine;
// branchTagKey(b) marks a claim as a member of branch b's closure.
const spineRevKey = "_rev"

func branchTagKey(branch string) string { return "_b_" + branch }

// tagBranchClosure walks head's closure level by level (one bulk tag-read per
// hop), stamping branchTagKey(branch)=rev on every claim that lacks it and
// pruning where it is present — so the oldest revision to reach a claim wins.
// New tags accumulate in pending (TagArchive flushes per revision); already-
// flushed revisions are seen through GetClaimTags. ids keeps an Id per string
// key so pending can be handed back to SetClaimsTags.
func tagBranchClosure(ctx context.Context, u Universe, branch string, head Id, rev string,
	pending map[string]map[string]string, ids map[string]Id,
) error {
	key := branchTagKey(branch)
	frontier := []Id{head}
	for len(frontier) > 0 {
		tags, err := u.GetClaimTags(ctx, frontier)
		if err != nil {
			return err
		}
		var fresh []Id
		for i, id := range frontier {
			s := id.String()
			if pending[s][key] != "" || tags[i][key] != "" {
				continue // already a member (this run or a prior one) — prune
			}
			if pending[s] == nil {
				pending[s] = map[string]string{}
				ids[s] = id
			}
			pending[s][key] = rev
			fresh = append(fresh, id)
		}
		if len(fresh) == 0 {
			return nil
		}
		claims, err := u.GetClaims(ctx, fresh, WithNotDiffMaterialized())
		if err != nil {
			return err
		}
		var next []Id
		seen := map[string]struct{}{}
		for _, c := range claims {
			for _, e := range c.Edges() {
				r := e.Reference()
				if r == nil {
					continue
				}
				if _, dup := seen[r.String()]; dup {
					continue
				}
				seen[r.String()] = struct{}{}
				next = append(next, r)
			}
		}
		frontier = next
	}
	return nil
}

// TagArchive descends a's branch-table spine to its root, then tags each
// revision's changed-branch closures oldest→newest, returning the head-id
// history the descent produced. Each branch-table claim gets a spine-revision
// tag in the same batch as its branch tags, so _rev is the commit marker and a
// later call resumes past the already-tagged prefix. History is the output of
// this walk, never an input.
func TagArchive(ctx context.Context, a Archive, ...TagAcvhiveOptions) ([]HistoryItem, error) {
	arc, ok := a.(*archive)
	if !ok {
		return nil, errNotArchive
	}
	u := arc.u

	// 1: descend the branch-table spine head→root via contribution/diff.
	// TODO: *until* we hit a claim that already has a revision
	var spine []Claim // newest→oldest
	for id := arc.bth.ID(); id != nil; {
		// NOTE: this can be done more efficient once we have queries
		bt, err := GetClaim(ctx, u, id, WithNotDiffMaterialized())
		if err != nil {
			return nil, err
		}
    // TODO: unless otion "retag all" is set
		if bt.Tag(spineRevKey) != "" {
			break
		}
		spine = append(spine, bt)
		id = nil
		for _, e := range bt.Edges(EdgeFilterType{Type: EdgeTypeDiff}) {
			id = e.Reference()
			break
		}
	}
	if len(spine) == 0 {
		return nil, nil
	}

	// 2: iterate root→head, assuming revisions 0..n.
	history := make([]HistoryItem, 0, len(spine))
	rev := uint64(0)
	for i := len(spine) - 1; i >= 0; i, rev = i-1, rev+1 {
		bt := spine[i]
		revStr := strconv.FormatUint(rev, 10)
		history = append(history, NewHistoryItem(bt.ID(), int(rev), bt.Node().CreatedAt()))
		if spineTags[i][spineRevKey] == revStr {
			continue // 2.1: already tagged at this revision — skip
		}
		// 2.2: the raw claim's branch edges are exactly the branches that moved
		// this revision; tag each one's closure.
		pending := map[string]map[string]string{}
		pids := map[string]Id{}
		for _, e := range bt.Edges(EdgeFilterType{Type: EdgeTypeBranch}) {
			name, err := e.GetField(FieldName)
			if err != nil {
				return nil, err
			}
			if err := tagBranchClosure(ctx, u, name, e.Reference(), revStr, pending, pids); err != nil {
				return nil, err
			}
		}
		// 2.3: stamp the spine revision in the same batch (the commit marker).
		s := bt.ID().String()
		if pending[s] == nil {
			pending[s] = map[string]string{}
			pids[s] = bt.ID()
		}
		pending[s][spineRevKey] = revStr

		claims := make([]Id, 0, len(pending))
		vals := make([]map[string]string, 0, len(pending))
		for k, t := range pending {
			claims = append(claims, pids[k])
			vals = append(vals, t)
		}
		if err := u.SetClaimsTags(ctx, nil, claims, vals); err != nil {
			return nil, err
		}
	}
	return history, nil
}
