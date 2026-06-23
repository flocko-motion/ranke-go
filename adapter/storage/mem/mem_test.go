package mem

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
)

// TestConformance runs the black-box Universe suite against the in-memory
// storage. mem has no medium-specific failure modes (no backing store to
// corrupt), so the generic suite is the whole story here.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe { return New() })
}
