// package: neo4j / query
// type:    adapter
// job:     the adapter's execution report — collect per-stage events above the requested
// level and finalise them into a ranke.QueryReport
// limits:  collection only; what to log and the truncation verdict are the query path's
// (-> query.go)
package neo4j

import (
	"time"

	"github.com/flocko-motion/ranke-go"
)

// reportBuilder collects execution events above the requested level; a zero level
// makes log/finalize no-ops.
type reportBuilder struct {
	level   ranke.ReportLevel
	started time.Time
	events  []ranke.QueryEvent
}

func newReport(level ranke.ReportLevel, started time.Time) *reportBuilder {
	return &reportBuilder{level: level, started: started}
}

func (r *reportBuilder) on() bool { return reportRank(r.level) > 0 }

func (r *reportBuilder) log(engine, op string, level ranke.ReportLevel, dur time.Duration, detail string) {
	if !r.on() || reportRank(level) > reportRank(r.level) {
		return
	}
	r.events = append(r.events, ranke.QueryEvent{
		At: time.Since(r.started), Engine: engine, Op: op, Level: level, Duration: dur, Detail: detail,
	})
}

func (r *reportBuilder) finalize(results int, truncated bool) *ranke.QueryReport {
	if !r.on() {
		return nil
	}
	return &ranke.QueryReport{
		StartedAt: r.started, Elapsed: time.Since(r.started),
		Results: results, Truncated: truncated, Events: r.events,
	}
}

func reportRank(l ranke.ReportLevel) int {
	switch l {
	case ranke.ReportError:
		return 1
	case ranke.ReportWarn:
		return 2
	case ranke.ReportInfo:
		return 3
	case ranke.ReportDebug:
		return 4
	case ranke.ReportTrace:
		return 5
	default:
		return 0
	}
}
