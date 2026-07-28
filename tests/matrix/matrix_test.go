package matrix_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go/tests/matrix"
)

// TestMatrix is the full agreement matrix: every backend this host can stand up,
// compared against the mem reference over the whole RQL corpus. Rows needing a
// service that is not running here skip themselves, so the run is green on a bare
// checkout and grows teeth as services come up.
func TestMatrix(t *testing.T) {
	matrix.Run(t, matrix.Config{})
}

// TestMatrixBranchClosure is the minimal focus case: the smallest archive that
// still has multiple revisions and a claim shared across them, and just the
// branch closure. It isolates branch membership — the _b_<branch> tag set a
// native backend scans must equal the reference's closure walk.
func TestMatrixBranchClosure(t *testing.T) {
	matrix.Run(t, matrix.Config{Size: 2, Only: "closure/branch"})
}

// TestMatrixReverseWalk guards the forward-then-reverse path at a size dense
// enough that a source is reached only via the deriver citing it — the case a
// single-trail lowering drops but a per-step frontier keeps. It passes at the
// default size, so it needs the larger graph to be meaningful.
func TestMatrixReverseWalk(t *testing.T) {
	matrix.Run(t, matrix.Config{Size: 55, Only: "path/uses-of-sources"})
}
