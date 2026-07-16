// package: ranke / tagger
// type:    logic
// job:     TagArchive — descend an archive's branch-table spine and stamp every
// branch's closure with each member claim's .node.Height()
// limits:  optional (gated by Capabilities.Tags); tags are read off the claims (GetClaims injects them) and written through the Universe's bulk SetClaimsTags — this file holds no backend I/O of its own
package ranke

import (
	"context"
	"strconv"
)

// The tag keys this tagger owns (SpineRevKey, BranchTagKey, BranchTagGlob) are
// defined in universe_taxonomy.go, the central reserved-namespace registry.

// TagArchiveOptions tunes TagArchive.
type TagArchiveOptions struct {
	// RetagAll ignores the resume bound and descends to the spine root,
	// re-tagging every revision from 0.
	RetagAll bool
}

// tagBranchClosure walks head's closure level by level, recording
// BranchTagKey(branch)=height for every claim that lacks it and pruning where
// it is present — so the oldest revision to reach a claim wins. It reads
// membership off the claim it already fetches (GetClaims injects tags) or the
// pending map, and accumulates new tags into tags (keyed by claim-id string) for
// TagArchive to flush once per revision. The prune stops descending at an
// already-tagged claim (whose closure is tagged too).
func tagBranchClosure(ctx context.Context, u Universe, branch string, head Id, height int, tags map[string]map[string]string) error {
	key := BranchTagKey(branch)
	val := strconv.Itoa(height)
	frontier := []Id{head}
	for len(frontier) > 0 {
		claims, err := u.GetClaims(ctx, frontier, WithNotDiffMaterialized())
		if err != nil {
			return err
		}
		var next []Id
		seen := map[string]struct{}{}
		for _, c := range claims {
			s := c.ID().String()
			if c.Tag(key) != "" || tags[s][key] != "" {
				continue // already a member (prior run or this revision) — prune
			}
			if tags[s] == nil {
				tags[s] = map[string]string{}
			}
			tags[s][key] = val
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

// TagArchive descends a's branch-table spine and tags each revision's branch
// closures oldest→newest
func TagArchive(ctx context.Context, a Archive, opts ...TagArchiveOptions) ([]HistoryItem, error) {
	arc, ok := a.(*archive)
	if !ok {
		return nil, errNotArchive
	}
	u := arc.u
	var o TagArchiveOptions
	if len(opts) > 0 {
		o = opts[0]
	}

	// 1: descend the branch-table spine head→root
	var spine []Claim // newest→oldest
	base := uint64(0)
	for id := arc.bth.ID(); id != nil; {
		// NOTE: this can be done more efficiently once we have queries.
		bt, err := GetClaim(ctx, u, id, WithNotDiffMaterialized())
		if err != nil {
			return nil, err
		}
		if r := bt.Tag(SpineRevKey); r != "" && !o.RetagAll {
			k, err := strconv.ParseUint(r, 10, 64)
			if err != nil {
				return nil, err
			}
			base = k + 1
			break
		}
		spine = append(spine, bt)
		id = nil
		for _, e := range bt.Edges(EdgeFilterTypes{Types: []string{EdgeTypeDiff, EdgeTypeBranches}}) {
			id = e.Reference()
			break
		}
	}
	if len(spine) == 0 {
		return nil, nil // already tagged up to the head — nothing new
	}

	// 2: iterate the untagged spine oldest→newest, revisions base..n.
	history := make([]HistoryItem, 0, len(spine))
	revision := base
	for i := len(spine) - 1; i >= 0; i, revision = i-1, revision+1 {
		bt := spine[i]
		revisionStr := strconv.FormatUint(revision, 10)
		history = append(history, NewHistoryItem(bt.ID(), int(revision), int(bt.Node().Height()), bt.Node().CreatedAt()))

		// 2.1: accumulate every branch's membership for this revision into one
		// shared map, so a claim reached by several branches gets each
		// _b_<branch> before the single flush. The value is the branch table's
		// height at this revision.
		tags := map[string]map[string]string{}
		for _, e := range bt.Edges(EdgeFilterType{Type: EdgeTypeBranch}) {
			name, err := e.GetField(FieldName)
			if err != nil {
				return nil, err
			}
			if err := tagBranchClosure(ctx, u, name, e.Reference(), int(bt.Node().Height()), tags); err != nil {
				return nil, err
			}
		}
		// 2.2: add the spine revision on the branch-table claim, then flush the
		// revision at once — clearing _b* first (so a hidden branch's stale tags
		// go) and writing each claim's accumulated set. _br is the commit marker.
		s := bt.ID().String()
		if tags[s] == nil {
			tags[s] = map[string]string{}
		}
		tags[s][SpineRevKey] = revisionStr
		if err := u.SetClaimsTags(ctx, []string{BranchTagGlob}, tags); err != nil {
			return nil, err
		}
	}
	return history, nil
}
