// package: storage / persistence
// type:    adapter
// job:     shared default Copy* walkers every concrete adapter can delegate to
// limits:  no storage of its own; concrete backends live in sub-packages (-> adapter/fs, adapter/mem)
//
// Package storage holds persistence adapters for a ranke Universe and the
// default building blocks they share. Each concrete adapter lives in its
// own sub-package (adapter/fs, adapter/mem, ...) and implements
// ranke.Universe strictly against ranke's public API. The shared default
// Copy* walkers live here so every adapter can delegate to them.
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/flocko-motion/ranke-go"
)

// DefaultCopyClaims is the reference CopyClaims for adapters that don't
// need a native fast path. It walks claim-by-claim via ranke's public
// single-item helpers; cloud backends should provide their own batched
// implementation. See ranke.Universe.CopyClaims for the option semantics.
//
// Progress is best-effort: as a naive walker it cannot know a closure's
// size in advance, so DiscoveryComplete stays false until the walk
// drains, then flips true with the final callback.
func DefaultCopyClaims(ctx context.Context, dst, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	if dst == nil {
		return errors.New("adapter.DefaultCopyClaims: nil dst")
	}
	if src == nil {
		return errors.New("adapter.DefaultCopyClaims: nil src")
	}
	cfg := ranke.NewCopyConfig(opts...)

	var prog ranke.CopyProgress
	report := func() {
		if cfg.Progress != nil {
			cfg.Progress(prog)
		}
	}

	visited := map[string]struct{}{}
	queue := make([]ranke.Id, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			return errors.New("adapter.DefaultCopyClaims: nil id")
		}
		queue = append(queue, id)
	}

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

		// A present claim is treated as fully copied — under the merge
		// invariant a claim never exists without its closure (and, for
		// copies made WithContent, its content). This is what makes the
		// walk idempotent and lets re-runs short-circuit.
		has, err := ranke.HasClaim(ctx, dst, next)
		if err != nil {
			return fmt.Errorf("dst.HasClaim %s: %w", k, err)
		}
		if has {
			continue
		}

		c, err := ranke.GetClaim(ctx, src, next)
		if err != nil {
			return fmt.Errorf("src.GetClaim %s: %w", k, err)
		}
		if err := ranke.PutClaim(ctx, dst, c); err != nil {
			return fmt.Errorf("dst.PutClaim %s: %w", k, err)
		}
		prog.ClaimsCopied++

		if cfg.Content {
			if ch := c.Node().ContentHash(); ch != nil {
				b, err := ranke.GetContent(ctx, src, ch, c.Node().Size())
				if err != nil {
					return fmt.Errorf("src.GetContent %s: %w", ch.String(), err)
				}
				if err := ranke.PutContent(ctx, dst, ch, b); err != nil {
					return fmt.Errorf("dst.PutContent %s: %w", ch.String(), err)
				}
				prog.BytesCopied += uint64(len(b))
			}
		}

		if cfg.Closure {
			for _, e := range c.Edges() {
				queue = append(queue, e.Reference())
			}
		}

		prog.ClaimsRemaining = uint64(len(queue))
		report()
	}

	prog.ClaimsRemaining = 0
	prog.BytesRemaining = 0
	prog.DiscoveryComplete = true
	report()
	return nil
}

// DefaultCopyContents is the reference CopyContents for adapters without a
// native fast path. It copies blob-by-blob via ranke's public single-item
// helpers, skipping any the receiver already has. WithClosure/WithContent
// are ignored; WithProgress is honored.
func DefaultCopyContents(ctx context.Context, dst, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	if dst == nil {
		return errors.New("adapter.DefaultCopyContents: nil dst")
	}
	if src == nil {
		return errors.New("adapter.DefaultCopyContents: nil src")
	}
	cfg := ranke.NewCopyConfig(opts...)

	var prog ranke.CopyProgress
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ref.Hash == nil {
			return errors.New("adapter.DefaultCopyContents: nil hash")
		}
		has, err := ranke.HasContent(ctx, dst, ref.Hash)
		if err != nil {
			return fmt.Errorf("dst.HasContent %s: %w", ref.Hash.String(), err)
		}
		if !has {
			b, err := ranke.GetContent(ctx, src, ref.Hash, ref.Size)
			if err != nil {
				return fmt.Errorf("src.GetContent %s: %w", ref.Hash.String(), err)
			}
			if err := ranke.PutContent(ctx, dst, ref.Hash, b); err != nil {
				return fmt.Errorf("dst.PutContent %s: %w", ref.Hash.String(), err)
			}
			prog.BytesCopied += uint64(len(b))
		}
		prog.ClaimsRemaining = uint64(len(refs) - i - 1)
		if cfg.Progress != nil {
			cfg.Progress(prog)
		}
	}

	prog.ClaimsRemaining = 0
	prog.DiscoveryComplete = true
	if cfg.Progress != nil {
		cfg.Progress(prog)
	}
	return nil
}
