// package: ranke / deletion
// type:    logic
// job:     the planned-deletion sweep — remove the bytes of every claim whose delete_by has
// fallen due, leaving the gap the citing edges already explain (`R-DPLANNED`)
// limits:  the planned form only; a requested deletion is the Sequencer's to carry out
// (`R-DREQUEST`). Removes bytes through the Universe port and rewrites no id
package ranke

import (
	"context"
	"time"
)

// SweepResult is one sweep's outcome: the claims whose bytes were removed, and the
// content blobs that went with them.
type SweepResult struct {
	Claims   []Id
	Contents []Id
}

// DeletePlanned removes the bytes of every claim in the closure of heads whose
// delete_by has fallen due at now (`R-DPLANNED`), and is idempotent.
//
// It never deletes the four `R-DSTRUCT` subtypes, whatever a field says; CheckDeletable
// is that set. The gap it leaves is explained by the date the citing edges already
// copied (`R-DGAP`), and external content goes with the claim.
func DeletePlanned(ctx context.Context, u Universe, heads []Id, now time.Time) (SweepResult, error) {
	var out SweepResult
	if !u.Capabilities().Delete {
		return out, ErrUnsupported
	}
	seen := map[string]struct{}{}
	queue := append([]Id(nil), heads...)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		id := queue[0]
		queue = queue[1:]
		if id == nil {
			continue
		}
		k := id.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}

		c, err := GetClaim(ctx, u, id, WithNotDiffMaterialized())
		if err != nil {
			continue // already gone, or unreadable: a sweep reports no gap of its own
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
		due, err := dueForDeletion(c, now)
		if err != nil {
			return out, err
		}
		if !due {
			continue
		}
		out.Claims = append(out.Claims, id)
		if h := c.Node().GetContentHash(); h != nil {
			out.Contents = append(out.Contents, h)
		}
	}
	if len(out.Claims) > 0 {
		if err := u.DeleteClaims(ctx, out.Claims); err != nil {
			return out, err
		}
	}
	if len(out.Contents) > 0 {
		if err := u.DeleteContents(ctx, out.Contents); err != nil {
			return out, err
		}
	}
	return out, nil
}

// dueForDeletion reports whether c's own delete_by has fallen due at now. A claim the
// `R-DSTRUCT` set covers is never due, and one carrying a delete_by it may not carry
// is an error rather than a silent pass — the field is there, so something is wrong.
func dueForDeletion(c Claim, now time.Time) (bool, error) {
	n := c.Node()
	v, err := n.GetField(FieldDeleteBy)
	if err != nil {
		return false, nil // no schedule
	}
	if err := CheckDeletable(NodeClass(n.TypeClass()), n.TypeSub(), map[string]string{FieldDeleteBy: v}); err != nil {
		return false, err
	}
	due, err := parseRFC3339Nano(v) // `V-TIME`; a decoded claim has already passed this
	if err != nil {
		return false, WithDetail(ErrTimestampForm, FieldDeleteBy+"="+v)
	}
	return !due.After(now), nil
}
