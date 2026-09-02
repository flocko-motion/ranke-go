// package: neo4j / stream
// type:    adapter
// job:     hand the rows Cypher already filtered, ordered and limited out as a ResultStream,
// the report last, and tell a spent limit.time from a failed read
// limits:  streams what the statement returned; lowering and running it are query.go's
// (-> query, report)
package neo4j

import (
	"errors"
	"strings"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/rankegraph/ranke-go"
)

// cypherStream is neo4j's ResultStream over an already-resolved slice: Cypher ran
// the filter/order/limit, so this hands rows out in order, the report last.
type cypherStream struct {
	results []ranke.QueryResult
	i       int
}

func (s *cypherStream) Next() bool {
	if s.i < len(s.results) {
		s.i++
		return true
	}
	return false
}
func (s *cypherStream) Result() ranke.QueryResult  { return s.results[s.i-1] }
func (s *cypherStream) Report() *ranke.QueryReport { return ranke.ReportOf(s.results) }
func (s *cypherStream) Err() error                 { return nil }
func (s *cypherStream) Close() error               { return nil }

// boundedByTime reports whether err is limit.time being spent rather than the read
// failing: a budget was set, the caller's context is still live, and the server ended
// the transaction on the budget the statement carried.
func boundedByTime(err error, budget time.Duration, ctxErr error) bool {
	return budget > 0 && ctxErr == nil && isTxTimeout(err)
}

// isTxTimeout reports whether err is the server ending the transaction on its
// timeout, which is how limit.time arrives back here. Only the TransactionTimedOut
// codes count: the driver normalises every transient termination — an operator's
// killTransaction, a leader switch, a lock manager stopping — into a bare
// Transaction.Terminated, and reading those as a bounded answer would turn each of
// them into a silent empty success.
func isTxTimeout(err error) bool {
	var nerr *neo4jdriver.Neo4jError
	return errors.As(err, &nerr) && strings.Contains(nerr.Code, "TransactionTimedOut")
}
