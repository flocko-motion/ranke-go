package minimal

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
)

// TestConformance proves the minimum-viable adapter still satisfies the
// full Universe contract — all the heavy lifting comes from
// storage.NewBlobUniverse.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe { return New() })
}
