package ranke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every rule id cited in the code must exist in the spec. A typo'd id is worse
// than no id: it points confidently at nothing, and a conformance case citing the
// real rule would never lead anyone to the check that should have caught it.

// specEnv names an already-fetched spec, for working offline or against one not
// published yet.
const specEnv = "RANKE_SPEC"

// citation matches a rule id in the form the code writes it: backticked, so prose
// mentioning a word like V-SHAPED is not mistaken for one.
var citation = regexp.MustCompile("`([VR]-[A-Z0-9]+)`")

// specPath resolves the spec: RANKE_SPEC, the copy `make docs` fetches, else the
// local one .gitignore reserves at the repo root.
func specPath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv(specEnv),
		filepath.Join("docs", "papers", "spec", "ranke-spec.typ"),
		"specification.typ",
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("no spec found — run `make docs`, or point %s at an extracted copy. "+
		"Not checking the citations is worse than a red run: a typo'd id survives silently.", specEnv)
	return ""
}

// TestCitedRuleIdsExist reads every id the code cites and requires the spec to
// define it. Ids are the spec's to allocate, so one that is absent is either a typo
// or an id invented here, and both point at nothing.
func TestCitedRuleIdsExist(t *testing.T) {
	spec, err := os.ReadFile(specPath(t))
	require.NoError(t, err)
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`rule\("([VR]-[A-Z0-9]+)"`).FindAllStringSubmatch(string(spec), -1) {
		defined[m[1]] = true
	}
	require.NotEmpty(t, defined, "the spec defines no rules — wrong file?")

	cited := map[string][]string{} // id → files citing it
	require.NoError(t, filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range citation.FindAllStringSubmatch(string(src), -1) {
			cited[m[1]] = append(cited[m[1]], path)
		}
		return nil
	}))
	require.NotEmpty(t, cited, "no citations found — the walk is looking in the wrong place")

	for id, files := range cited {
		require.Truef(t, defined[id], "%s is cited in %v but no rule of that id is in the spec",
			id, files)
	}
}
