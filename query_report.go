// package: ranke / query_report
// type:    logic
// job:     the RQL execution report — a structured, timestamped event log a query collects
//
//	across the whole call chain when Execution.Report is set (returned via ResultStream.Report)
//
// limits:  a passive collector carried in the context; every station logs into it, and a nil
//
//	collector (report off) makes every log a no-op, so the hot path pays nothing
package ranke

import (
	"context"
	"sync"
	"time"
)

// ReportLevel classifies a QueryEvent.
type ReportLevel string

// Report levels are one ordered scale (Error < Warn < Info < Debug < Trace);
// Execution.Report sets the threshold and every event at or above it is kept.
// Empty means no report.
const (
	ReportError ReportLevel = "error" // failures — always logged when a report is requested
	ReportWarn  ReportLevel = "warn"  // fallbacks, caps hit, recoverable issues
	ReportInfo  ReportLevel = "info"  // high-level stages: select, filter, sort, results
	ReportDebug ReportLevel = "debug" // routing decisions, per-layer hits, engine lowering (e.g. Cypher)
	ReportTrace ReportLevel = "trace" // per-claim / per-edge steps — exhaustive
)

// reportRank orders the levels for threshold comparison; 0 is off/unknown.
func reportRank(l ReportLevel) int {
	switch l {
	case ReportError:
		return 1
	case ReportWarn:
		return 2
	case ReportInfo:
		return 3
	case ReportDebug:
		return 4
	case ReportTrace:
		return 5
	default:
		return 0
	}
}

// QueryReport is a query's execution log (Execution.Report set), returned via
// ResultStream.Report. Engine/layer identity is per-event, since one query can
// span several engines.
type QueryReport struct {
	StartedAt time.Time     // wall clock at query start
	Elapsed   time.Duration // total execution time
	Results   int           // items emitted
	Truncated bool          // whether Limit cut the read short
	Events    []QueryEvent  // the ordered, multi-engine execution log
}

// QueryEvent is one logged step or point during execution.
type QueryEvent struct {
	At       time.Duration  // offset from QueryReport.StartedAt
	Engine   string         // who emitted it: "native", "cypher", "stack", "partition", …
	Op       string         // what it did: "load-root", "step", "filter", "sort", "route", "lower-cypher", …
	Level    ReportLevel    // info | warn | error
	Duration time.Duration  // elapsed for a timed step; 0 for a point event
	Detail   string         // human message, or the lowered query text (e.g. Cypher)
	Attrs    map[string]any // structured extras: layer/shard name, depth, edge/result counts, …
}

// reportCollector accumulates QueryEvents during one query. It is carried in
// the context (reportContext / reportFrom), so every station on the call chain
// — across Universe.Query boundaries, nested reads, and router fan-out — logs
// into the same one. A nil *reportCollector is the "report off" case: every
// method is a no-op, so when Execution.Report is false the query pays nothing.
// All methods are safe for concurrent use (routers may fan out).
type reportCollector struct {
	mu      sync.Mutex
	started time.Time
	level   ReportLevel // requested verbosity threshold
	events  []QueryEvent
}

func newReportCollector(started time.Time, level ReportLevel) *reportCollector {
	return &reportCollector{started: started, level: level}
}

// enabled reports whether an event at level would be kept under the collector's
// threshold. A nil / off collector returns false, so stations skip building
// expensive detail (a lowered query string, per-claim attrs) they won't log.
func (r *reportCollector) enabled(level ReportLevel) bool {
	if r == nil {
		return false
	}
	t := reportRank(r.level)
	return t > 0 && reportRank(level) <= t
}

// log records a point event, subject to the threshold. A nil / below-threshold
// collector is a no-op.
func (r *reportCollector) log(engine, op string, level ReportLevel, detail string, attrs map[string]any) {
	if !r.enabled(level) {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, QueryEvent{
		At:     time.Since(r.started),
		Engine: engine,
		Op:     op,
		Level:  level,
		Detail: detail,
		Attrs:  attrs,
	})
	r.mu.Unlock()
}

// timed records an event that began at start, stamping its Duration. A nil
// receiver is a no-op. Pair it with a start captured just before the work:
//
//	t := reportStart(rc); … ; rc.timed("native", "step", t, "", attrs)
func (r *reportCollector) timed(engine, op string, level ReportLevel, start time.Time, detail string, attrs map[string]any) {
	if !r.enabled(level) {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, QueryEvent{
		At:       time.Since(r.started), // recorded when the step finished
		Engine:   engine,
		Op:       op,
		Level:    level,
		Duration: time.Since(start), // how long the step took
		Detail:   detail,
		Attrs:    attrs,
	})
	r.mu.Unlock()
}

// finalize snapshots the collected events into a QueryReport, stamping the
// totals. A nil receiver returns nil (report was off).
func (r *reportCollector) finalize(results int, truncated bool) *QueryReport {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]QueryEvent, len(r.events))
	copy(events, r.events)
	return &QueryReport{
		StartedAt: r.started,
		Elapsed:   time.Since(r.started),
		Results:   results,
		Truncated: truncated,
		Events:    events,
	}
}

// reportStart returns a start time for a timed step only when rc is live, so a
// report-off query does no timekeeping. It's fine to pass the zero Time to
// timed (which no-ops on a nil receiver anyway).
func reportStart(rc *reportCollector) time.Time {
	if rc == nil {
		return time.Time{}
	}
	return time.Now()
}

// --- context carriage ------------------------------------------------------

type reportKey struct{}

// reportContext returns ctx carrying rc, so every station on the call chain
// logs into the same collector.
func reportContext(ctx context.Context, rc *reportCollector) context.Context {
	return context.WithValue(ctx, reportKey{}, rc)
}

// reportFrom returns the collector carried in ctx, or nil when reporting is off
// — the signal a station uses to skip logging work entirely.
func reportFrom(ctx context.Context) *reportCollector {
	rc, _ := ctx.Value(reportKey{}).(*reportCollector)
	return rc
}

// beginReport creates-or-reuses the collector for a query. When Execution.Report
// is set and no collector is yet in ctx (this is the outermost participant), it
// makes one, attaches it to a derived ctx, and returns created=true — that caller
// owns finalising the report onto its ResultStream. An inner participant finds
// the existing collector and returns created=false, so it logs but does not
// finalise. When reporting is off it returns (ctx, nil, false).
func beginReport(ctx context.Context, level ReportLevel, started time.Time) (context.Context, *reportCollector, bool) {
	if rc := reportFrom(ctx); rc != nil {
		return ctx, rc, false
	}
	if reportRank(level) == 0 {
		return ctx, nil, false
	}
	rc := newReportCollector(started, level)
	return reportContext(ctx, rc), rc, true
}

// ReportEnabled reports whether the query on ctx is collecting events at level.
// A storage adapter, router, or engine calls it to guard building expensive
// report detail (a lowered query string, per-item attrs) before ReportEvent.
// False when no report is active or level is below the requested threshold.
func ReportEnabled(ctx context.Context, level ReportLevel) bool {
	return reportFrom(ctx).enabled(level)
}

// ReportEvent logs one execution event into the query report carried by ctx, if
// any — a no-op when no report is active or level is below the threshold. This
// is the hook every participant uses to record its part of a query: which layer
// a router sent a read to, the Cypher a backend lowered to, a cache hit/miss.
func ReportEvent(ctx context.Context, engine, op string, level ReportLevel, detail string, attrs map[string]any) {
	reportFrom(ctx).log(engine, op, level, detail, attrs)
}
