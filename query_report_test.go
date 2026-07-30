package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for the RQL execution report (query_report.go): the structured event
// log a query collects when Execution.Report is set, and the zero-overhead
// no-op path when it isn't.

// opsOf collects the Op of every event, and indexes events by Op.
func opsOf(rep *QueryReport) map[string]QueryEvent {
	m := map[string]QueryEvent{}
	for _, e := range rep.Events {
		m[e.Op] = e
	}
	return m
}

// TestReportRecordsExecutionLog: a reported query produces a structured,
// engine-attributed, ordered log covering the reference engine's stages.
func TestReportRecordsExecutionLog(t *testing.T) {
	u, _, a, b := queryFixture(t) // root(0) ← a:source(1) ← b:entity(2)
	rs, err := u.Query(context.Background(), Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID(), Claim: b.ID(),
			Path: []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}}},
		Execution: Execution{Report: ReportTrace},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.True(t, got[0].ClaimNative.ID().Equal(a.ID()))

	rep := rs.Report()
	require.NotNil(t, rep)
	require.False(t, rep.StartedAt.IsZero(), "start time stamped")
	require.Equal(t, 1, rep.Results)
	require.NotEmpty(t, rep.Events)

	by := opsOf(rep)
	for _, op := range []string{"select", "load-start", "step", "filter", "sort", "results"} {
		e, ok := by[op]
		require.True(t, ok, "event %q present", op)
		require.Equal(t, "native", e.Engine, "engine on %q", op)
	}
	// The traversal step is timed and carries structured attrs.
	require.Equal(t, ReportInfo, by["step"].Level)
	require.NotNil(t, by["step"].Attrs["reached"])
	// The filter records how many candidates survived.
	require.NotNil(t, by["filter"].Attrs["kept"])
	// Events are ordered by their offset from start.
	for i := 1; i < len(rep.Events); i++ {
		require.GreaterOrEqual(t, rep.Events[i].At, rep.Events[i-1].At, "events in time order")
	}
}

// TestReportLevelThreshold: the level is a verbosity threshold — Info records
// the high-level stages but not per-claim fetches; Trace adds them.
func TestReportLevelThreshold(t *testing.T) {
	u, _, _, b := queryFixture(t)
	fetchEvents := func(level ReportLevel) int {
		rs, err := u.Query(context.Background(), Query{
			Select:    Select{Branch: BranchUniverse, Head: b.ID()},
			Execution: Execution{Report: level},
		}, Scope{Branch: BranchUniverse})
		require.NoError(t, err)
		_ = drain(t, rs)
		n := 0
		for _, e := range rs.Report().Events {
			if e.Op == "fetch" {
				n++
			}
		}
		return n
	}
	require.Zero(t, fetchEvents(ReportInfo), "Info omits per-claim fetch (Trace) events")
	require.Positive(t, fetchEvents(ReportTrace), "Trace records per-claim fetch events")
}

// TestReportOffIsNil: without Execution.Report the stream carries no report and
// the collector path is a no-op (nothing to assert but that Report() is nil).
func TestReportOffIsNil(t *testing.T) {
	u, _, _, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	_ = drain(t, rs)
	require.Nil(t, rs.Report(), "no report when Execution.Report is false")
}

// TestReportFilterCounts: the filter event reports input vs kept, so a selective
// Where is visible in the log.
func TestReportFilterCounts(t *testing.T) {
	u, _, a, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select:    Select{Branch: BranchUniverse, Head: b.ID()}, // full closure: root, a, b
		Where:     &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
		Execution: Execution{Report: ReportTrace},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.True(t, got[0].ClaimNative.ID().Equal(a.ID()))

	f := opsOf(rs.Report())["filter"]
	require.Equal(t, 3, f.Attrs["in"], "closure had 3 claims")
	require.Equal(t, 1, f.Attrs["kept"], "only the source survived the Where")
}
