package performance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/generator"
)

// CompareSuite is a black-box conformance check: it builds the SAME deterministic
// archive into two adapters and runs the SAME queries against both — their result
// sets (ids, in order) must be identical. Two adapters that both implement the
// spec must agree; a divergence is a conformance bug in at least one of them.
// Adapter-agnostic: openA/openB are opaque factories (mem, a neo4j stack, …).
type CompareSuite struct {
	suite.Suite
	nameA, nameB string
	openA, openB func() (ranke.Universe, func(), error)
	size         int    // generator size (0 → 5, the default matrix size)
	only         string // when set, run only the query with this name (focus mode)

	ctx      context.Context
	a, b     ranke.Universe
	cleanupA func()
	cleanupB func()
	m        *generator.Manifest
	head     ranke.Id // the "main" branch head (identical in both — content-addressed)
}

func (s *CompareSuite) SetupSuite() {
	s.ctx = context.Background()
	size := s.size
	if size == 0 {
		size = 5
	}
	spec := generator.SpecForSize(1, size)
	s.a, s.cleanupA, s.m, s.head = s.build(spec, s.openA)
	var headB ranke.Id
	s.b, s.cleanupB, _, headB = s.build(spec, s.openB)
	// Deterministic generation must yield byte-identical ids in both adapters —
	// otherwise the same-query comparison would be meaningless.
	s.Require().Equal(s.head.String(), headB.String(), "branch head must be identical across adapters")
}

func (s *CompareSuite) TearDownSuite() {
	if s.cleanupA != nil {
		s.cleanupA()
	}
	if s.cleanupB != nil {
		s.cleanupB()
	}
}

// build generates the archive into a fresh adapter and returns it, its cleanup,
// manifest, and the "main" branch head.
func (s *CompareSuite) build(spec generator.Spec, open func() (ranke.Universe, func(), error)) (ranke.Universe, func(), *generator.Manifest, ranke.Id) {
	u, cleanup, err := open()
	s.Require().NoError(err)
	m, err := generator.Generate(s.ctx, u, spec)
	s.Require().NoError(err)
	arc, err := ranke.NewArchive(s.ctx, u, m.Head)
	s.Require().NoError(err)
	_, err = ranke.TagArchive(s.ctx, arc)
	s.Require().NoError(err)
	br, err := arc.GetBranch(s.ctx, queryBranch)
	s.Require().NoError(err)
	return u, cleanup, m, br.Head()
}

// TestQueries runs each perf query against both adapters and asserts the result
// sets are identical (ids in order — order is part of the contract). Each query
// is a sub-test, so all divergences surface in one run.
func (s *CompareSuite) TestQueries() {
	for _, nq := range perfQueries(s.m, s.head) {
		if s.only != "" && nq.name != s.only {
			continue
		}
		s.Run(nq.name, func() {
			idsA := s.query(s.a, nq.q)
			idsB := s.query(s.b, nq.q)
			s.Equalf(idsA, idsB, "%s vs %s: %s returned %d ids, %s returned %d — result sets differ",
				s.nameA, s.nameB, s.nameA, len(idsA), s.nameB, len(idsB))
		})
	}
}

// query runs q against u (resolving the branch scope like an Archive would) and
// returns the reached ids in emitted order.
func (s *CompareSuite) query(u ranke.Universe, q ranke.Query) []string {
	scope, err := queryScope(s.ctx, u, q, s.m.Head)
	s.Require().NoError(err)
	rs, err := u.Query(s.ctx, q, scope)
	s.Require().NoError(err)
	var out []string
	for rs.Next() {
		out = append(out, rs.Result().Claim.ID().String())
	}
	s.Require().NoError(rs.Err())
	_ = rs.Close()
	return out
}

// TestConformanceMemVsNeo4j checks the neo4j/mem stack against the mem reference:
// same archive, same queries, identical results. Needs a neo4j (RANKE_NEO4J_BOLT).
func TestConformanceMemVsNeo4j(t *testing.T) {
	if !neo4jRequested() && !neo4jReachable() {
		t.Skip("no neo4j reachable (start one: services/neo4j.sh native up)")
	}
	suite.Run(t, &CompareSuite{
		nameA: "mem", openA: openMem,
		nameB: "neo4j/mem", openB: stacked(openNeo4j, openMem),
	})
}

// TestConformanceBranchClosure is the minimal focus case: the smallest archive
// that still has multiple revisions and a shared claim across branches, and just
// the one branch/closure query. It isolates branch-membership tagging — the
// _b_<branch> set neo4j scans must equal the reference's closure walk.
func TestConformanceBranchClosure(t *testing.T) {
	if !neo4jRequested() && !neo4jReachable() {
		t.Skip("no neo4j reachable (start one: services/neo4j.sh native up)")
	}
	suite.Run(t, &CompareSuite{
		nameA: "mem", openA: openMem,
		nameB: "neo4j/mem", openB: stacked(openNeo4j, openMem),
		size: 2, only: "branch/closure",
	})
}

// TestConformanceReverseWalk guards the forward-then-reverse path (uses-of-
// sources) at a size dense enough that a source is reached only via the deriver
// that cites it — the case a single-trail Cypher lowering would drop but the
// per-step frontier keeps. It passes at the small default size, so it needs the
// larger graph to be meaningful.
func TestConformanceReverseWalk(t *testing.T) {
	if !neo4jRequested() && !neo4jReachable() {
		t.Skip("no neo4j reachable (start one: services/neo4j.sh native up)")
	}
	suite.Run(t, &CompareSuite{
		nameA: "mem", openA: openMem,
		nameB: "neo4j/mem", openB: stacked(openNeo4j, openMem),
		size: 55, only: "branch/uses-of-sources",
	})
}
