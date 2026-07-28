// package: tests/performance / integration
// type:    test
// job:     metered — a ranke.Universe decorator that records a latency sample per operation and reports exact distributions (avg/median/p90/p99/min/max), rolled up by read/write and broken down by operation
// limits:  measures at the Universe interface, so it counts operations, not raw backend round-trips or write byte-sizes (those need adapter-internal instrumentation). Samples are kept raw and percentiles computed by sorting: exact, no dependency, fine at test scale (~50k samples at size 2000).
package performance

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

type opKind int

const (
	kindRead opKind = iota
	kindWrite
)

// sample is one timed Universe operation. Keeping raw samples (rather than
// pre-aggregating) lets the report slice by any dimension — op, read/write,
// phase, and later by size — and compute exact percentiles.
type sample struct {
	phase string
	op    string
	kind  opKind
	items int
	dur   time.Duration
}

// metered wraps a Universe and times every call. Round-trips to the backend
// happen inside these methods; the decorator records one sample each, tagged
// with the current phase so reads/writes can be attributed to generate vs
// verify (etc.).
type metered struct {
	inner   ranke.Universe
	mu      sync.Mutex
	phase   string
	samples []sample
	// readHits counts how many times each claim id is fetched (across all
	// GetClaims). distinct ids vs total reads quantifies re-read redundancy —
	// the exact waste a contributor-pubkey cache would remove.
	readHits map[string]int
	// content bytes moved. Exact and raw (the []byte is right there). Claim
	// record bytes are NOT counted here — they need the blob seam, where the
	// actual stored delta is visible (a materialised claim would mis-count).
	contentBytesRead    int64
	contentBytesWritten int64
}

func newMetered(inner ranke.Universe) *metered {
	return &metered{inner: inner, phase: "init", readHits: map[string]int{}}
}

// setPhase labels every subsequent sample until the next call — the caller
// brackets Generate / Verify so the report can attribute I/O to each.
func (m *metered) setPhase(p string) {
	m.mu.Lock()
	m.phase = p
	m.mu.Unlock()
}

func (m *metered) rec(op string, kind opKind, items int, d time.Duration) {
	m.mu.Lock()
	m.samples = append(m.samples, sample{m.phase, op, kind, items, d})
	m.mu.Unlock()
}

func (m *metered) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	t := time.Now()
	c, err := m.inner.GetClaims(ctx, ids, opts...)
	d := time.Since(t)
	m.mu.Lock()
	for _, id := range ids {
		if id != nil {
			m.readHits[id.String()]++
		}
	}
	m.mu.Unlock()
	m.rec("GetClaims", kindRead, len(ids), d)
	return c, err
}

func (m *metered) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	t := time.Now()
	b, err := m.inner.GetClaimsRaw(ctx, ids)
	m.rec("GetClaimsRaw", kindRead, len(ids), time.Since(t))
	return b, err
}

func (m *metered) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	t := time.Now()
	err := m.inner.PutClaims(ctx, cs)
	m.rec("PutClaims", kindWrite, len(cs), time.Since(t))
	return err
}

func (m *metered) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	t := time.Now()
	b, err := m.inner.HasClaims(ctx, ids)
	m.rec("HasClaims", kindRead, len(ids), time.Since(t))
	return b, err
}

func (m *metered) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	t := time.Now()
	c, err := m.inner.GetContents(ctx, refs)
	d := time.Since(t)
	var n int64
	for _, blob := range c {
		n += int64(len(blob))
	}
	m.mu.Lock()
	m.contentBytesRead += n
	m.mu.Unlock()
	m.rec("GetContents", kindRead, len(refs), d)
	return c, err
}

func (m *metered) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	t := time.Now()
	err := m.inner.PutContents(ctx, blobs)
	d := time.Since(t)
	var n int64
	for _, blob := range blobs {
		n += int64(len(blob.Content))
	}
	m.mu.Lock()
	m.contentBytesWritten += n
	m.mu.Unlock()
	m.rec("PutContents", kindWrite, len(blobs), d)
	return err
}

func (m *metered) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	t := time.Now()
	b, err := m.inner.HasContents(ctx, hashes)
	m.rec("HasContents", kindRead, len(hashes), time.Since(t))
	return b, err
}

func (m *metered) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	t := time.Now()
	r, err := m.inner.StreamContent(ctx, hash, size)
	m.rec("StreamContent", kindRead, 1, time.Since(t)) // times opening the stream, not consuming it
	if err != nil {
		return r, err
	}
	// Count bytes as the caller consumes them (flushed to the tally on Close),
	// so streamed content reads are not invisible in the byte totals.
	return &countingReadCloser{r: r, m: m}, nil
}

// countingReadCloser tallies bytes read from a stream into the meter on Close.
type countingReadCloser struct {
	r io.ReadCloser
	m *metered
	n int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReadCloser) Close() error {
	c.m.mu.Lock()
	c.m.contentBytesRead += c.n
	c.m.mu.Unlock()
	return c.r.Close()
}

func (m *metered) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	t := time.Now()
	hs, err := m.inner.GetClaimHeights(ctx, ids)
	m.rec("GetClaimHeights", kindRead, len(ids), time.Since(t))
	return hs, err
}

func (m *metered) GetClaimTags(ctx context.Context, claims []ranke.Id) ([]map[string]string, error) {
	t := time.Now()
	tags, err := m.inner.GetClaimTags(ctx, claims)
	m.rec("GetClaimTags", kindRead, len(claims), time.Since(t))
	return tags, err
}

func (m *metered) SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error {
	t := time.Now()
	err := m.inner.SetClaimsTags(ctx, clearTags, tags)
	m.rec("SetClaimsTags", kindWrite, len(tags), time.Since(t))
	return err
}

func (m *metered) Tag(ctx context.Context, head ranke.Id) error {
	t := time.Now()
	err := m.inner.Tag(ctx, head)
	m.rec("Tag", kindWrite, 1, time.Since(t))
	return err
}

func (m *metered) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	t := time.Now()
	rs, err := m.inner.Query(ctx, q, scope)
	m.rec("Query", kindRead, 1, time.Since(t))
	return rs, err
}

func (m *metered) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return m.inner.CopyClaims(ctx, src, ids, opts...)
}

func (m *metered) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return m.inner.CopyContents(ctx, src, refs, opts...)
}

func (m *metered) Sync(ctx context.Context, src ranke.Universe, id ranke.Id) <-chan ranke.SyncResult {
	return m.inner.Sync(ctx, src, id)
}

func (m *metered) Capabilities() ranke.Capabilities { return m.inner.Capabilities() }
func (m *metered) Close() error                     { return m.inner.Close() }

// --- reporting ---------------------------------------------------------

// dist is an exact latency distribution over a set of samples.
type dist struct {
	n             int
	items         int
	avg, min, max time.Duration
	p50, p90, p99 time.Duration
}

func distOf(samples []sample) dist {
	if len(samples) == 0 {
		return dist{}
	}
	ds := make([]time.Duration, len(samples))
	var sum time.Duration
	var items int
	for i, s := range samples {
		ds[i] = s.dur
		sum += s.dur
		items += s.items
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	n := len(ds)
	return dist{
		n: n, items: items, avg: sum / time.Duration(n),
		min: ds[0], max: ds[n-1],
		p50: pct(ds, 50), p90: pct(ds, 90), p99: pct(ds, 99),
	}
}

// pct is the nearest-rank percentile of a sorted slice.
func pct(sorted []time.Duration, q int) time.Duration {
	n := len(sorted)
	idx := (q*n+99)/100 - 1 // ceil(q/100 * n) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// report prints the distributions: a TOTAL, WRITE and READ rollups, then a
// line per operation. Exact percentiles from the raw samples.
func (m *metered) report() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	byOp := map[string][]sample{}
	byPhaseKind := map[string][]sample{} // "phase/READ" | "phase/WRITE"
	phaseOrder := []string{}
	seenPhase := map[string]bool{}
	for _, s := range m.samples {
		byOp[s.op] = append(byOp[s.op], s)
		pk := s.phase + "/" + kindName(s.kind)
		byPhaseKind[pk] = append(byPhaseKind[pk], s)
		if !seenPhase[s.phase] {
			seenPhase[s.phase] = true
			phaseOrder = append(phaseOrder, s.phase)
		}
	}
	ops := make([]string, 0, len(byOp))
	for op := range byOp {
		ops = append(ops, op)
	}
	sort.Strings(ops)

	b := &strBuilder{}
	line := func(label string, d dist) {
		if d.n == 0 {
			return
		}
		b.printf("\n  %-17s %7s %7s %s %s %s %s %s %s",
			label, intCell(d.n), intCell(d.items),
			durCell(d.avg), durCell(d.p50), durCell(d.p90), durCell(d.p99), durCell(d.min), durCell(d.max))
	}
	// Read-redundancy: total claim fetches vs distinct ids. The gap is the
	// re-read waste a contributor-pubkey cache (ranke.Cache) would remove; the
	// hottest id is the shared contributor fetched once per attributed claim.
	var totalReads, hottest int
	var hottestID string
	for id, n := range m.readHits {
		totalReads += n
		if n > hottest {
			hottest, hottestID = n, id
		}
	}
	distinct := len(m.readHits)
	factor := 0.0
	if distinct > 0 {
		factor = float64(totalReads) / float64(distinct)
	}
	var writeOps int
	for _, s := range m.samples {
		if s.kind == kindWrite {
			writeOps++
		}
	}
	b.printf("  round-trips=%d  reads=%d writes=%d  content in=%s out=%s",
		len(m.samples), totalReads, writeOps,
		bytesStr(m.contentBytesRead), bytesStr(m.contentBytesWritten))
	if distinct > 0 {
		short := hottestID
		if len(short) > 12 {
			short = short[:12] + "…"
		}
		b.printf("\n  claim-reads: %d over %d distinct (%.1f× re-read) — hottest %s ×%d",
			totalReads, distinct, factor, short, hottest)
	}
	// Table header + rule, aligned over the right-aligned value cells.
	header := fmt.Sprintf("\n  %-17s %7s %7s %9s %9s %9s %9s %9s %9s",
		"operation", "n", "items", "avg", "p50", "p90", "p99", "min", "max")
	ruleWidth := len(header) - 3 // drop the leading "\n  "
	b.printf("%s\n  %s", header, strings.Repeat("─", ruleWidth))
	// section prints a blank line then a "label ────" divider spanning the table,
	// separating the grand total, the per-chapter sequence, and the per-operation
	// summary so the three readings don't run together.
	section := func(label string) {
		dashes := ruleWidth - len(label) - 1
		if dashes < 0 {
			dashes = 0
		}
		b.printf("\n\n  %s %s", label, strings.Repeat("─", dashes))
	}
	line("TOTAL", distOf(m.samples))
	// By chapter, in sequence: reads and writes separately, so the cost of each
	// chapter's walk is visible on its own.
	section("by chapter (in sequence)")
	for _, p := range phaseOrder {
		line(p+"/WRITE", distOf(byPhaseKind[p+"/WRITE"]))
		line(p+"/READ", distOf(byPhaseKind[p+"/READ"]))
	}
	// By operation, aggregated across all chapters.
	section("by operation (totals)")
	for _, op := range ops {
		line(op, distOf(byOp[op]))
	}

	// Outliers — the slowest few samples with their position within their
	// phase/kind sequence (e.g. "#500/546"), so a fat tail reads as clustered
	// at the start (warm-up), evenly spaced (periodic flush), or scattered
	// (retry/GC). Only shown when the slowest is meaningfully above the median.
	b.outliers(m.samples)
	return b.String()
}

// outliers appends the top-K slowest samples, each located by its ordinal
// within its own phase+kind stream, so "when" a stall happened is visible.
func (b *strBuilder) outliers(samples []sample) {
	if len(samples) < 8 {
		return
	}
	type located struct {
		phase string
		kind  opKind
		pos   int
		total int
		dur   time.Duration
	}
	seq := map[string]int{}   // phase/kind → running count (position)
	total := map[string]int{} // phase/kind → total count
	for _, s := range samples {
		total[s.phase+kindName(s.kind)]++
	}
	locs := make([]located, len(samples))
	for i, s := range samples {
		key := s.phase + kindName(s.kind)
		locs[i] = located{s.phase, s.kind, seq[key], total[key], s.dur}
		seq[key]++
	}
	sort.Slice(locs, func(i, j int) bool { return locs[i].dur > locs[j].dur })
	// Split by kind so a fat WRITE tail is visible on its own, not buried under
	// the (expected) expensive closure reads.
	emit := func(label string, want opKind) {
		shown := 0
		var line string
		for _, l := range locs {
			if l.kind != want {
				continue
			}
			line += fmt.Sprintf(" %s@%s#%d/%d", durStr(l.dur), l.phase, l.pos, l.total)
			if shown++; shown == 5 {
				break
			}
		}
		if shown > 0 {
			b.printf("\n  slowest %-6s:%s", label, line)
		}
	}
	emit("writes", kindWrite)
	emit("reads", kindRead)
}

func kindName(k opKind) string {
	if k == kindWrite {
		return "WRITE"
	}
	return "READ"
}

// phaseIO returns the read and write operation counts recorded in a phase —
// the "reads used by this step" metric, the yardstick a caching change should
// visibly shrink.
func (m *metered) phaseIO(phase string) (reads, writes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.samples {
		if s.phase != phase {
			continue
		}
		if s.kind == kindWrite {
			writes++
		} else {
			reads++
		}
	}
	return reads, writes
}

func intCell(n int) string { return fmt.Sprintf("%d", n) }

// bytesStr renders a byte count in the largest unit that keeps it readable.
func bytesStr(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// durWidth is the visible width every latency cell is right-aligned to, so
// columns line up whether the value is "0", "482ns", "723.3µs" or "218.34ms".
const durWidth = 9

// ANSI colours follow the scale, so the unit reads at a glance: ns white
// (negligible), µs grey, ms yellow (worth noticing), s red (alarming).
// Disabled when NO_COLOR is set.
const (
	cReset  = "\033[0m"
	cGrey   = "\033[90m"   // nanoseconds — smallest, dim but legible
	cWhite  = "\033[97m"   // microseconds
	cYellow = "\033[33m"   // milliseconds
	cRed    = "\033[1;31m" // seconds — largest, loudest
)

var useColor = os.Getenv("NO_COLOR") == ""

// durStr formats a duration to a single unit (ns/µs/ms/s) with the unit at the
// end, so the eye reads magnitude by the suffix.
func durStr(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1fµs", float64(d.Nanoseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// durCell right-aligns durStr(d) to durWidth (padding the plain text, so
// invisible colour codes never break alignment) and colours it by scale.
func durCell(d time.Duration) string {
	s := fmt.Sprintf("%*s", durWidth, durStr(d))
	if !useColor {
		return s
	}
	var c string
	switch {
	case d < time.Microsecond:
		c = cGrey // ns — no problem
	case d < time.Millisecond:
		c = cWhite // µs — ok
	case d < time.Second:
		c = cYellow // ms — warning
	default:
		c = cRed // s — problem
	}
	return c + s + cReset
}

type strBuilder struct{ s string }

func (b *strBuilder) printf(f string, a ...any) { b.s += fmt.Sprintf(f, a...) }
func (b *strBuilder) String() string            { return b.s }
