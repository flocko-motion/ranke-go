// package: ranke / query_report
// type:    logic
// job:     the RQL execution report — a structured event log a query collects across the call chain
// when Execution.Report is set
// limits:  a passive collector carried in ctx; a nil collector (report off) makes every log a no-op
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

// reportCollector accumulates a query's events, carried in ctx so every station
// logs into one. nil = report off (all methods no-op). Safe for concurrent use.
type reportCollector struct {
	mu      sync.Mutex
	started time.Time
	level   ReportLevel // requested verbosity threshold
	events  []QueryEvent
}

func newReportCollector(started time.Time, level ReportLevel) *reportCollector {
	return &reportCollector{started: started, level: level}
}

// enabled reports whether an event at level clears the threshold (nil = off).
func (r *reportCollector) enabled(level ReportLevel) bool {
	if r == nil {
		return false
	}
	t := reportRank(r.level)
	return t > 0 && reportRank(level) <= t
}

// log records a point event if it clears the threshold.
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

// timed records an event, stamping how long it took (since start).
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

// finalize snapshots the events into a QueryReport with totals (nil = off).
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

// reportStart returns a timer start only when rc is live (else no timekeeping).
func reportStart(rc *reportCollector) time.Time {
	if rc == nil {
		return time.Time{}
	}
	return time.Now()
}

// --- context carriage ------------------------------------------------------

type reportKey struct{}

// reportContext returns ctx carrying rc for the whole call chain.
func reportContext(ctx context.Context, rc *reportCollector) context.Context {
	return context.WithValue(ctx, reportKey{}, rc)
}

// reportFrom returns the collector in ctx, or nil when reporting is off.
func reportFrom(ctx context.Context) *reportCollector {
	rc, _ := ctx.Value(reportKey{}).(*reportCollector)
	return rc
}

// beginReport creates the collector (returning created=true) if reporting is on
// and none is in ctx yet; otherwise reuses the one in ctx (created=false). The
// creator owns finalising it.
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

// ReportEnabled reports whether ctx is collecting events at level — the guard a
// backend uses before building expensive report detail.
func ReportEnabled(ctx context.Context, level ReportLevel) bool {
	return reportFrom(ctx).enabled(level)
}

// ReportEvent logs one event into the report on ctx (no-op if off) — the hook a
// router or engine uses to record its part of a query.
func ReportEvent(ctx context.Context, engine, op string, level ReportLevel, detail string, attrs map[string]any) {
	reportFrom(ctx).log(engine, op, level, detail, attrs)
}

// WithReport returns a ctx collecting events at or above level, plus a snapshot
// func. It makes the log readable for calls no ResultStream covers — which layer
// of a stack served a GetClaims, what a copy walk visited. An outer collector, if
// ctx already has one, is reused and its events included.
func WithReport(ctx context.Context, level ReportLevel) (context.Context, func() []QueryEvent) {
	ctx, rc, _ := beginReport(ctx, level, time.Now())
	return ctx, func() []QueryEvent {
		if rep := rc.finalize(0, false); rep != nil {
			return rep.Events
		}
		return nil
	}
}
