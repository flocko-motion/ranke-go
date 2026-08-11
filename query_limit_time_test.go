package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// limit.time in the reference executor. R-QLIMIT calls a read cut short by either
// bound a complete answer to the query as bounded, so the two must arrive the same
// way: results, plus the report saying it was cut short. A deadline surfacing as an
// error would leave a caller unable to tell "the answer, bounded" from "the read
// failed".

// chain builds a forward chain of n derivations off one root, long enough that a
// walk down it takes measurable time to finish.
func chain(t *testing.T, n int) (Universe, Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	require.NoError(t, PutClaim(ctx, u, root))

	prev := srcClaim(t, root, "base")
	require.NoError(t, PutClaim(ctx, u, prev))
	for i := 0; i < n; i++ {
		next := entityClaim(t, root, "person", "link", prev)
		require.NoError(t, PutClaim(ctx, u, next))
		prev = next
	}
	return u, prev
}

// TestLimitTimeTruncatesRatherThanErroring is the acceptance case: a budget too
// small to finish yields what the read reached and reports truncation.
func TestLimitTimeTruncatesRatherThanErroring(t *testing.T) {
	u, head := chain(t, 400)
	q := Query{
		Select:    Select{Branch: BranchUniverse, Head: head.ID()},
		Limit:     Limit{Time: time.Nanosecond}, // spent before the walk starts
		Execution: Execution{Report: ReportInfo},
	}

	rs, err := u.Query(context.Background(), q, Scope{Branch: BranchUniverse})
	require.NoError(t, err, "a read cut short by limit.time is an answer, not an error")
	got := drain(t, rs)

	rep := rs.Report()
	require.NotNil(t, rep)
	require.True(t, rep.Truncated, "the report must say the read was cut short")
	require.Less(t, len(got), 400, "a budget this small cannot have reached the whole chain")
	require.Equal(t, len(got), rep.Results)
}

// TestLimitResultsTruncatesTheSameWay is the other bound, asserted through the same
// surface — the point being that a caller reads truncation one way for both.
func TestLimitResultsTruncatesTheSameWay(t *testing.T) {
	u, head := chain(t, 20)
	q := Query{
		Select:    Select{Branch: BranchUniverse, Head: head.ID()},
		Limit:     Limit{Results: 5},
		Execution: Execution{Report: ReportInfo},
	}

	rs, err := u.Query(context.Background(), q, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)

	rep := rs.Report()
	require.NotNil(t, rep)
	require.True(t, rep.Truncated)
	require.Len(t, got, 5)
	require.Equal(t, 5, rep.Results)
}

// TestLimitTimeZeroIsUnbounded: 0 means unbounded, so a read that would exceed any
// small budget still completes and reports no truncation.
func TestLimitTimeZeroIsUnbounded(t *testing.T) {
	u, head := chain(t, 200)
	q := Query{
		Select:    Select{Branch: BranchUniverse, Head: head.ID()},
		Limit:     Limit{Time: 0},
		Execution: Execution{Report: ReportInfo},
	}

	rs, err := u.Query(context.Background(), q, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)

	require.False(t, rs.Report().Truncated, "an unbounded read is not cut short")
	require.Len(t, got, 202, "the whole chain: 200 links, the base source, the contributor")
}

// TestCallerCancellationIsStillAnError draws the line the budget must not blur. A
// caller cancelling is a failed read; only limit.time running out bounds the answer.
func TestCallerCancellationIsStillAnError(t *testing.T) {
	u, head := chain(t, 400)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := u.Query(ctx, Query{
		Select: Select{Branch: BranchUniverse, Head: head.ID()},
		Limit:  Limit{Time: time.Hour}, // ample, so only the cancellation can stop it
	}, Scope{Branch: BranchUniverse})
	require.ErrorIs(t, err, context.Canceled)
}
