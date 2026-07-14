package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit test for the copy-option resolution (WithClosure /
// WithContent / WithProgress → CopyConfig). The Universe interface itself
// is adapter-backed; the option plumbing is pure and pinned here.

func TestNewCopyConfigDefaults(t *testing.T) {
	cfg := NewCopyConfig()
	require.False(t, cfg.Closure, "closure off by default")
	require.False(t, cfg.Content, "content off by default")
	require.Nil(t, cfg.Progress)
}

func TestNewCopyConfigOptions(t *testing.T) {
	var called bool
	cfg := NewCopyConfig(WithClosure(), WithContent(), WithProgress(func(CopyProgress) { called = true }))
	require.True(t, cfg.Closure, "WithClosure sets closure")
	require.True(t, cfg.Content, "WithContent sets content")
	require.NotNil(t, cfg.Progress, "WithProgress registers a callback")

	cfg.Progress(CopyProgress{})
	require.True(t, called, "the registered callback is the one supplied")
}
