// package: client / transport
// type:    adapter
// job:     POST /contribute — a claim set and the external content it names, sent as one CBOR
// sequence, with the content gathered from the Universe rather than left to the caller
// limits:  builds and posts the stream; framing it is the library's (-> codec_wire), and admitting
// it is the server's Sequencer
package client

import (
	"bytes"
	"context"
	"errors"

	"github.com/flocko-motion/ranke-go"
)

// ContributionResult is what the merge produced: the new branch-table head and the
// ids absorbed under it.
type ContributionResult struct {
	Head string   `json:"head"`
	Ids  []string `json:"ids"`
}

// Contribute merges claims into branch atomically, with the content they address.
//
// It takes a Universe so the blobs cannot be forgotten: a claim carries only its
// content_hash, and a re-run's dedup reads that hash rather than fetching it, so
// claims sent alone leave the bytes behind and nothing downstream notices.
func (c *Client) Contribute(ctx context.Context, u ranke.Universe, branch string, claims []ranke.Claim) (*ContributionResult, error) {
	if branch == "" {
		return nil, ranke.ErrWireNoBranch
	}
	if len(claims) == 0 {
		return &ContributionResult{}, nil
	}
	refs, err := ExternalContent(claims)
	if err != nil {
		return nil, err
	}
	blobs, err := fetchContent(ctx, u, refs)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, ranke.WireConstraints{
		Branches:     []string{branch},
		Referencable: []string{branch},
	})
	for _, cl := range claims {
		if err := w.WriteClaim(branch, cl); err != nil {
			return nil, err
		}
	}
	for _, b := range blobs {
		if err := w.WriteContent(b); err != nil {
			return nil, err
		}
	}

	out := &ContributionResult{}
	err = c.json(ctx, request{
		method: "POST", path: "/contribute",
		body: buf.Bytes(), send: "application/cbor-seq",
	}, out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExternalContent lists each distinct blob the claims address, edges included: an
// edge holds content of its own. Order is first appearance, so a stream reproduces.
func ExternalContent(claims []ranke.Claim) ([]ranke.ContentRef, error) {
	var refs []ranke.ContentRef
	seen := map[string]bool{}
	add := func(hash ranke.Id, size uint64) {
		if hash == nil || seen[hash.String()] {
			return
		}
		seen[hash.String()] = true
		refs = append(refs, ranke.ContentRef{Hash: hash, ContentSize: size})
	}
	for _, cl := range claims {
		if cl == nil {
			return nil, ErrNilClaim
		}
		n := cl.Node()
		add(n.GetContentHash(), n.GetContentSize())
		for _, e := range cl.Edges() {
			add(e.GetContentHash(), e.GetContentSize())
		}
	}
	return refs, nil
}

// fetchContent reads each blob from the Universe the claims were built against.
func fetchContent(ctx context.Context, u ranke.Universe, refs []ranke.ContentRef) ([]ranke.ContentBlob, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if u == nil {
		return nil, ErrNoUniverse
	}
	data, err := u.GetContents(ctx, refs)
	if err != nil {
		// A blob the Universe cannot answer for is the failure this exists to catch,
		// so it arrives under a name that says so — with the cause kept, since a
		// backend that is merely unreachable reports the same absence.
		if errors.Is(err, ranke.ErrNotFound) {
			return nil, errors.Join(ErrContentMissing, err)
		}
		return nil, err
	}
	blobs := make([]ranke.ContentBlob, 0, len(refs))
	for i, ref := range refs {
		if i >= len(data) || data[i] == nil {
			return nil, ranke.WithDetail(ErrContentMissing, ref.Hash.String())
		}
		blobs = append(blobs, ranke.ContentBlob{Hash: ref.Hash, Content: data[i]})
	}
	return blobs, nil
}
