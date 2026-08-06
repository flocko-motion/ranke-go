// package: ranke / query
// type:    logic
// job:     R-QCONTENT — how many bytes of inline content each encoded claim carries, read off
// Output.Content and spent by the codec
// limits:  inline content only, per §Content the bytes a record holds; external content stays in
// the Universe, which is what lets content in full be S(v) (R-QCANON)
package ranke

// contentBudget is the inline content one encoded claim may still carry, spent in
// canonical order — the node's content, then its edges'. A nil budget inlines every
// claim's content in full, leaving a stored record byte-identical.
type contentBudget struct {
	left     int
	overflow Overflow
}

// newContentBudget reads Output.Content as R-QCONTENT states it: Max 0 inlines content
// in full, an absent Content inlines none, and any higher Max is the cap in bytes. Each
// claim gets its own, the cap being per claim.
func newContentBudget(oc *OutputContent) *contentBudget {
	switch {
	case oc == nil:
		return &contentBudget{}
	case oc.Max == 0:
		return nil
	default:
		return &contentBudget{left: oc.Max, overflow: oc.Overflow}
	}
}

// inFull reports whether b leaves content as stored, so an encode may hand back the
// bytes it holds rather than building the record again.
func (b *contentBudget) inFull() bool { return b == nil }

// take returns the run of content the budget affords and spends it. Content within the
// cap passes whole; past it, cutoff keeps the bytes up to the cap and omit keeps none
// (R-QCONTENT). content_size is unaffected, so a claim still declares what it holds.
func (b *contentBudget) take(content []byte) []byte {
	if b == nil || len(content) == 0 {
		return content
	}
	if len(content) <= b.left {
		b.left -= len(content)
		return content
	}
	if b.overflow == OverflowCutoff && b.left > 0 {
		kept := content[:b.left]
		b.left = 0
		return kept
	}
	return nil
}
