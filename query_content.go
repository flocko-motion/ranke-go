// package: ranke / query
// type:    logic
// job:     `R-QCONTENT` — how many bytes of inline content an encoded claim carries, read off
// Output.Content and spent by the codec across the claim's content sequence
// limits:  inline content only, per §Content the bytes a record holds; external content stays in
// the Universe, which is what lets content in full be S(v) (`R-QCANON`)
package ranke

// contentBudget is the inline content one encoded claim may still carry, spent along the
// claim's content sequence — its edges' content in S(v)'s order, then the node's, edge
// content being the smaller part — and what arrives is a prefix of it. A nil budget
// inlines content in full.
type contentBudget struct {
	left    int
	cutoff  bool // inline a partial value at the cap
	stopped bool // the prefix has ended
}

// newContentBudget reads Output.Content per `R-QCONTENT`: Max 0 inlines in full, an absent
// Content inlines none, an absent Overflow is omit. One budget per claim.
func newContentBudget(oc *OutputContent) *contentBudget {
	switch {
	case oc == nil:
		return &contentBudget{}
	case oc.Max == 0:
		return nil
	default:
		return &contentBudget{left: oc.Max, cutoff: oc.Overflow == OverflowCutoff}
	}
}

// inFull reports whether b leaves content as stored, so an encode may reuse those bytes.
func (b *contentBudget) inFull() bool { return b == nil }

// take returns the content the budget affords and spends it. At the cap the prefix ends:
// cutoff keeps the bytes up to it, omit keeps none of that value, and nothing follows.
func (b *contentBudget) take(content []byte) []byte {
	switch {
	case b == nil:
		return content
	case len(content) == 0:
		return content // carries none, so the sequence passes through it
	case b.stopped:
		return nil
	case len(content) <= b.left:
		b.left -= len(content)
		return content
	}
	b.stopped = true
	if b.cutoff && b.left > 0 {
		kept := content[:b.left]
		b.left = 0
		return kept
	}
	return nil
}
