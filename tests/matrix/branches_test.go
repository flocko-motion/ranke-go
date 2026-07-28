package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// TestBranchesArePopulated: the generated archive carries several branches, each
// holding claims. Branch-scoped reads mean nothing on an archive with one branch.
func TestBranchesArePopulated(t *testing.T) {
	u, cleanup, err := backends.Reference().Open()
	require.NoError(t, err)
	defer cleanup()

	m, err := generator.Generate(context.Background(), u, generator.SpecForSize(1, 5))
	require.NoError(t, err)
	require.Greater(t, len(m.Branches), 1, "the archive names more than one branch")

	for _, name := range m.Branches {
		require.NotEmpty(t, branchMembers(t, u, m.Head, name),
			"branch %q holds claims", name)
	}
}

// TestBranchIsolation: a branch read returns that branch's members and leaves the
// others' exclusive claims out.
func TestBranchIsolation(t *testing.T) {
	eachToyBackend(t, generator.ToyBranches(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		require.Greater(t, len(m.Branches), 1, "needs more than one branch to isolate")

		members := map[string][]string{}
		for _, name := range m.Branches {
			members[name] = branchMembers(t, u, m.Head, name)
		}
		for _, name := range m.Branches {
			exclusive := exclusiveTo(name, members)
			require.NotEmpty(t, exclusive, "branch %q holds claims no other branch does", name)
			for _, other := range m.Branches {
				if other == name {
					continue
				}
				for _, id := range exclusive {
					require.NotContains(t, members[other], id,
						"branch %q leaves out %q's exclusive claim", other, name)
				}
			}
		}
	})
}

// branchMembers scans one branch and returns the ids it holds.
func branchMembers(t *testing.T, u ranke.Universe, archiveHead ranke.Id, branch string) []string {
	t.Helper()
	answer, err := rql.Run(context.Background(), u, ranke.Query{
		Select: ranke.Select{Branch: branch},
	}, archiveHead)
	require.NoError(t, err)
	out := make([]string, 0, len(answer))
	for _, fp := range answer {
		out = append(out, firstField(fp))
	}
	return out
}

// exclusiveTo returns the ids only this branch holds.
func exclusiveTo(branch string, members map[string][]string) []string {
	elsewhere := map[string]bool{}
	for name, ids := range members {
		if name == branch {
			continue
		}
		for _, id := range ids {
			elsewhere[id] = true
		}
	}
	var out []string
	for _, id := range members[branch] {
		if !elsewhere[id] {
			out = append(out, id)
		}
	}
	return out
}
