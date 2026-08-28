package neo4j

import (
	"context"
	"errors"
	"testing"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// limit.time in the native lowering. R-QLIMIT makes a read cut short by either bound
// a complete answer to the query as bounded, so this layer reports it the way the
// reference does rather than surfacing a failure.
//
// The server enforces the budget at its own check interval, which is coarse — see
// TestTxTimeoutTerminatesALongTransaction. A sub-second read therefore finishes
// whatever budget it carries, so an end-to-end truncation cannot be provoked
// reliably at test scale; these cover the mechanism and the decision instead.

// TestTxTimeoutConfigurerIsOptIn: the bound travels with the statement only when
// there is one, and 0 stays unbounded — asserted here rather than through the
// server, since "no timeout" has no timing signature to observe.
func TestTxTimeoutConfigurerIsOptIn(t *testing.T) {
	require.Nil(t, txTimeout(0), "limit.time 0 is unbounded, so no configurer")
	require.Nil(t, txTimeout(-time.Second), "a negative budget is not a bound either")
	require.Len(t, txTimeout(time.Second), 1, "a budget travels as one transaction configurer")
}

// TestTxTimeoutTerminatesALongTransaction proves the budget reaches the server and
// that isTxTimeout recognises what comes back. The statement is a four-way product
// that would run for hours, so the timeout decides the outcome and nothing races it.
func TestTxTimeoutTerminatesALongTransaction(t *testing.T) {
	ctx := context.Background()
	u, _ := connectTestNeo4j(t)
	_, err := u.query(ctx, "UNWIND range(1,2000) AS i CREATE (:probe {i:i})", nil)
	require.NoError(t, err)

	_, err = u.query(ctx,
		"MATCH (a:probe),(b:probe),(c:probe),(d:probe) WHERE a.i+b.i+c.i+d.i > 0 RETURN count(*) AS n",
		nil, txTimeout(200*time.Millisecond)...)
	require.Error(t, err, "the server must end a transaction that outlives its budget")
	require.True(t, isTxTimeout(err), "and that ending must be recognised: %v", err)
	require.True(t, boundedByTime(err, 200*time.Millisecond, nil), "so the read is bounded, not failed")
}

// TestBoundedByTimeDecision is the truth table the Query path turns on. Only a
// server-side termination under a budget the caller did not cancel is a bounded
// answer; everything else is a failure the caller must see.
func TestBoundedByTimeDecision(t *testing.T) {
	timedOut := &neo4jdriver.Neo4jError{Code: "Neo.ClientError.Transaction.TransactionTimedOutClientConfiguration"}
	other := &neo4jdriver.Neo4jError{Code: "Neo.ClientError.Statement.SyntaxError"}

	require.True(t, boundedByTime(timedOut, time.Second, nil))
	require.False(t, boundedByTime(timedOut, 0, nil), "no budget means nothing was bounded")
	require.False(t, boundedByTime(timedOut, time.Second, context.Canceled),
		"the caller cancelling is a failed read, whatever the server said")
	require.False(t, boundedByTime(other, time.Second, nil), "an unrelated error is still an error")
	require.False(t, boundedByTime(errors.New("dial tcp: refused"), time.Second, nil))

	// The driver normalises every transient termination into a bare Terminated — an
	// operator's killTransaction, a leader switch, a lock manager stopping. Reading
	// those as a bounded answer would turn each into a silent empty success.
	killed := &neo4jdriver.Neo4jError{Code: "Neo.ClientError.Transaction.Terminated"}
	require.False(t, boundedByTime(killed, time.Second, nil),
		"a bare Terminated is the failure it is, not limit.time being spent")
}

// TestLimitResultsReportsTruncation: the cap is served by the statement, and the
// extra row it asks for is what tells the caller more existed — the same Truncated
// signal limit.time raises, which is the point of reporting them alike.
func TestLimitResultsReportsTruncation(t *testing.T) {
	ctx := context.Background()
	u, head := openTestNeo4j(t)

	full := readAll(t, ctx, u, head, 0)
	require.Greater(t, len(full), 3, "the cap has to be smaller than the answer to bite")

	rs, err := u.Query(ctx, ranke.Query{
		Select:    ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Limit:     ranke.Limit{Results: 3},
		Execution: ranke.Execution{Report: ranke.ReportInfo},
	}, ranke.Scope{Branch: ranke.BranchUniverse})
	require.NoError(t, err)

	require.Equal(t, 3, countResults(t, rs), "the cap is honoured, and the extra row never reaches the caller")
	require.True(t, rs.Report().Truncated)
	require.NoError(t, rs.Close())
}

// TestLimitResultsAtTheAnswerIsNotTruncated: a cap the answer fits inside cuts
// nothing short, so the extra row the statement asks for must not read as one.
func TestLimitResultsAtTheAnswerIsNotTruncated(t *testing.T) {
	ctx := context.Background()
	u, head := openTestNeo4j(t)

	full := readAll(t, ctx, u, head, 0)
	rs, err := u.Query(ctx, ranke.Query{
		Select:    ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Limit:     ranke.Limit{Results: len(full)},
		Execution: ranke.Execution{Report: ranke.ReportInfo},
	}, ranke.Scope{Branch: ranke.BranchUniverse})
	require.NoError(t, err)

	require.Equal(t, len(full), countResults(t, rs))
	require.False(t, rs.Report().Truncated, "a cap exactly at the answer cuts nothing short")
	require.NoError(t, rs.Close())
}

// countResults drains rs and returns how many results it carried. The report is an
// element of the sequence, not a result (`R-QSTREAM`), so a cap on results does not
// bound it and a count of them does not include it.
func countResults(t *testing.T, rs ranke.ResultStream) int {
	t.Helper()
	n := 0
	for rs.Next() {
		if rs.Result().Kind != ranke.KindReport {
			n++
		}
	}
	require.NoError(t, rs.Err())
	return n
}

// readAll runs the universe-scoped closure read at limit and returns the ids.
func readAll(t *testing.T, ctx context.Context, u ranke.Universe, head ranke.Id, limit int) []string {
	t.Helper()
	rs, err := u.Query(ctx, ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Limit:  ranke.Limit{Results: limit},
	}, ranke.Scope{Branch: ranke.BranchUniverse})
	require.NoError(t, err)
	var out []string
	for rs.Next() {
		out = append(out, rs.Result().ClaimId.String())
	}
	require.NoError(t, rs.Err())
	require.NoError(t, rs.Close())
	return out
}
