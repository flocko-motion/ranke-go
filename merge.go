package ranke

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMergeClosure walks the closure of head in src claim-by-claim
// via LoadClaim and writes each into dst. Idempotent (content-addressed);
// dst.HasClaim short-circuits anything already present. Used as the
// MergeClosure implementation by backends that don't need a native
// fast path (mem, fs). Cloud backends should provide their own.
func DefaultMergeClosure(ctx context.Context, dst, src Universe, head Id) error {
	if dst == nil {
		return errors.New("ranke.DefaultMergeClosure: nil dst")
	}
	if src == nil {
		return errors.New("ranke.DefaultMergeClosure: nil src")
	}
	if head == nil {
		return errors.New("ranke.DefaultMergeClosure: nil head")
	}
	visited := map[string]struct{}{}
	queue := []Id{head}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := queue[0]
		queue = queue[1:]
		k := id.String()
		if _, seen := visited[k]; seen {
			continue
		}
		visited[k] = struct{}{}

		has, err := dst.HasClaim(ctx, id)
		if err != nil {
			return fmt.Errorf("dst.HasClaim %s: %w", k, err)
		}
		if has {
			continue
		}

		c, err := src.LoadClaim(ctx, id)
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
