package ranke

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMergeClaim walks the provenance of id in src claim-by-claim
// via LoadClaim and writes each into dst. Idempotent (content-addressed);
// dst.HasClaim short-circuits anything already present. Used as the
// MergeClaim implementation by backends that don't need a native
// fast path (mem, fs). Cloud backends should provide their own.
func DefaultMergeClaim(ctx context.Context, dst, src Universe, id Id) error {
	if dst == nil {
		return errors.New("ranke.DefaultMergeClaim: nil dst")
	}
	if src == nil {
		return errors.New("ranke.DefaultMergeClaim: nil src")
	}
	if id == nil {
		return errors.New("ranke.DefaultMergeClaim: nil id")
	}
	visited := map[string]struct{}{}
	queue := []Id{id}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next := queue[0]
		queue = queue[1:]
		k := next.String()
		if _, seen := visited[k]; seen {
			continue
		}
		visited[k] = struct{}{}

		has, err := dst.HasClaim(ctx, next)
		if err != nil {
			return fmt.Errorf("dst.HasClaim %s: %w", k, err)
		}
		if has {
			continue
		}

		c, err := src.LoadClaim(ctx, next)
		if err != nil {
			return fmt.Errorf("src.LoadClaim %s: %w", k, err)
		}
		if err := dst.SaveClaim(ctx, c); err != nil {
			return fmt.Errorf("dst.SaveClaim %s: %w", k, err)
		}

		if ch := c.Node().ContentHash(); ch != nil {
			size := c.Node().Size()
			bytes, err := src.GetContent(ctx, ch, size)
			if err != nil {
				return fmt.Errorf("src.GetContent %s: %w", ch.String(), err)
			}
			if err := dst.SaveContent(ctx, ch, bytes); err != nil {
				return fmt.Errorf("dst.SaveContent %s: %w", ch.String(), err)
			}
		}

		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return nil
}
