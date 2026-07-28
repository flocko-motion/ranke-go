// package: tests/matrix / conformance
// type:    test
// job:     the cross-backend agreement matrix — build one deterministic archive into every available backend, run the shared RQL corpus against each, and assert every backend's answer matches the reference's
// limits:  correctness only, never timing (-> tests/performance); it asks whether an answer is right, never how a backend produced it — routing, lowering, and cache tiers are the backend's business
//
// Two backends implementing the read language must answer a query identically;
// a divergence is a conformance bug in at least one. Run it over your own rows
// with matrix.Run(t, Config{Rows: ...}).
package matrix

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/generator"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// defaultSize builds fast into every backend while still carrying every corner
// (revisions, diff chains, external blobs, both relation polarities).
const defaultSize = 5

// Config parameterises an agreement run.
type Config struct {
	// Reference is the engine every row is compared against. The zero value is
	// backends.Reference() — the in-memory store, which answers through the
	// library's reference executor.
	Reference *backends.Backend
	// Rows are the backends under test; nil means backends.All(). A row whose
	// name matches the reference is skipped (nothing is compared to itself).
	Rows []backends.Backend
	// Size is the generator size knob; 0 means defaultSize.
	Size int
	// Only narrows the run to the one corpus entry with this name — the focus
	// mode for isolating a single divergence. Empty runs the whole corpus.
	Only string
}

// Run builds one deterministic archive into the reference and every available
// row, then asserts each row answers the corpus identically, in stream order.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	ctx := context.Background()

	ref := backends.Reference()
	if cfg.Reference != nil {
		ref = *cfg.Reference
	}
	rows := cfg.Rows
	if rows == nil {
		rows = backends.All()
	}
	size := cfg.Size
	if size == 0 {
		size = defaultSize
	}
	spec := generator.SpecForSize(1, size)

	// The reference is the baseline every row is judged against, so its own
	// failure to open is fatal rather than a skip.
	refU, refCleanup, err := ref.Open()
	require.NoErrorf(t, err, "reference backend %q must be available", ref.Name)
	defer refCleanup()

	refM, refHead := build(t, ctx, refU, spec)
	queries := corpus(refM, refHead, cfg.Only)
	require.NotEmptyf(t, queries, "no corpus entry named %q", cfg.Only)

	refAnswers := make(map[string]rql.Answer, len(queries))
	for _, nq := range queries {
		answer, err := rql.Run(ctx, refU, nq.Q, refM.Head)
		require.NoErrorf(t, err, "reference %q: query %s", ref.Name, nq.Name)
		refAnswers[nq.Name] = answer
	}

	// A query matching nothing would make every row agree trivially, so the
	// corpus is checked for substance before any row is compared against it.
	t.Run("reference/corpus-is-substantive", func(t *testing.T) {
		for _, nq := range queries {
			if len(refAnswers[nq.Name]) == 0 {
				t.Errorf("%s matched no claim on the %s reference — the entry proves nothing; fix the query or the generator corner it targets\n  rql: %s",
					nq.Name, ref.Name, rql.Describe(nq.Q))
			}
		}
	})

	compared := 0
	for _, row := range rows {
		if row.Name == ref.Name {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			u, cleanup, err := row.Open()
			if errors.Is(err, backends.ErrUnavailable) {
				t.Skipf("unavailable here: %v", err)
			}
			require.NoError(t, err)
			defer cleanup()

			m, head := build(t, ctx, u, spec)
			// Content addressing makes generation reproducible: the same spec
			// must yield the same ids in every backend, or comparing answers to
			// the same query would be meaningless.
			require.Equalf(t, refHead.String(), head.String(),
				"branch head differs from the %s reference — the archives are not the same graph", ref.Name)

			compared++
			for _, nq := range queries {
				t.Run(nq.Name, func(t *testing.T) {
					got, err := rql.Run(ctx, u, nq.Q, m.Head)
					require.NoError(t, err)
					require.Equalf(t, refAnswers[nq.Name], got,
						"%s disagrees with %s\n  rql: %s\n  %s returned %d results, %s returned %d",
						row.Name, ref.Name, rql.Describe(nq.Q),
						ref.Name, len(refAnswers[nq.Name]), row.Name, len(got))
				})
			}
		})
	}

	if compared == 0 {
		t.Logf("no backend was available to compare against %s — the matrix proved nothing this run "+
			"(start services: services/neo4j.sh native up)", ref.Name)
	}
}

// corpus is the query set for a run, narrowed to a single entry in focus mode.
func corpus(m *generator.Manifest, root ranke.Id, only string) []rql.NamedQuery {
	all := rql.Corpus(m, root)
	if only == "" {
		return all
	}
	var out []rql.NamedQuery
	for _, nq := range all {
		if nq.Name == only {
			out = append(out, nq)
		}
	}
	return out
}

// build writes the archive into an open backend, returning its manifest and the
// "main" branch head. It prepares nothing: a backend needing an external call to
// become correct is itself the defect.
func build(t *testing.T, ctx context.Context, u ranke.Universe, spec generator.Spec) (*generator.Manifest, ranke.Id) {
	t.Helper()
	m, err := generator.Generate(ctx, u, spec)
	require.NoError(t, err)
	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, rql.Branch)
	require.NoError(t, err)
	return m, br.Head()
}
