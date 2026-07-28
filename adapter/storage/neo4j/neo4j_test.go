package neo4j_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/neo4j"
)

// TestTier checks the write tier: eager by default, overridable via WithTier.
// Capabilities is static, so no live graph db is needed.
func TestTier(t *testing.T) {
	if got := neo4j.New(nil).Capabilities().Tier; got != ranke.StorageTierEager {
		t.Fatalf("default tier = %q, want eager", got)
	}
	got := neo4j.New(nil, neo4j.WithTier(ranke.StorageTierLazy)).Capabilities().Tier
	if got != ranke.StorageTierLazy {
		t.Fatalf("WithTier(lazy) = %q, want lazy", got)
	}
}
