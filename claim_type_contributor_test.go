package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Contributor views (claim_type_contributor.go):
// the bare-*claim fallbacks (used when a contributor flows without being
// resolved via AsContributor) and the WithSigningKey wrapper's forwarding.

// TestBareClaimContributorFallbacks: an unwrapped contributor claim exposes
// the bare fallbacks — no signing key, and its pubkey is its inline content
// (§5.7). AsContributor is the way to attach a key / resolve an external
// pubkey; without it these are what a bare claim reports.
func TestBareClaimContributorFallbacks(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	c, err := NewClaim(NodeContributor, nil).WithInlineContent(pubkey).Sign(priv)
	require.NoError(t, err)

	bare, ok := c.(Contributor) // the bare *claim satisfies Contributor
	require.True(t, ok)
	require.Nil(t, bare.SigningKey(), "a bare claim carries no private key")
	require.Equal(t, pubkey, bare.Pubkey(), "bare pubkey is the inline content")
}

// TestSignedContributorWrapper: WithSigningKey wraps a Contributor, carrying
// the session key and the resolved pubkey while forwarding the rest.
func TestSignedContributorWrapper(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	c, err := NewClaim(NodeContributor, nil).WithInlineContent(pubkey).Sign(priv)
	require.NoError(t, err)
	bare := c.(Contributor)

	wrapped := WithSigningKey(bare, priv)
	require.Equal(t, priv, wrapped.SigningKey(), "carries the session key")
	require.Equal(t, pubkey, wrapped.Pubkey(), "falls back to the wrapped contributor's pubkey")
	require.True(t, wrapped.ID().Equal(c.ID()), "ID forwarded")
	require.True(t, wrapped.IsContributor(), "IsContributor forwarded")
	require.NotNil(t, wrapped.Node(), "Node forwarded")

	// AsContributor is forwarded and re-resolves against the (inline) pubkey.
	view, err := wrapped.AsContributor(context.Background(), nil, priv)
	require.NoError(t, err)
	require.True(t, view.ID().Equal(c.ID()))
}
