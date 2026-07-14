package ranke

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the lazy wrapErr (errors.go): Error() composes
// sentinel + detail + cause, and Unwrap exposes both so errors.Is matches
// either the sentinel or the underlying cause.

func TestWrapErrDetail(t *testing.T) {
	err := withDetail(ErrNotFound, "claim abc")
	require.ErrorIs(t, err, ErrNotFound, "matches the sentinel")
	require.Contains(t, err.Error(), ErrNotFound.Error())
	require.Contains(t, err.Error(), "claim abc", "detail appears in the message")
}

func TestWrapErrCause(t *testing.T) {
	cause := errors.New("disk gone")
	err := wrap(ErrNotFound, cause)
	require.ErrorIs(t, err, ErrNotFound, "matches the sentinel")
	require.ErrorIs(t, err, cause, "and the wrapped cause")
	require.Contains(t, err.Error(), "disk gone")
}

func TestWrapErrDetailAndCause(t *testing.T) {
	cause := errors.New("bad bytes")
	err := wrapDetail(ErrIntegrity, "content xyz", cause)
	require.ErrorIs(t, err, ErrIntegrity)
	require.ErrorIs(t, err, cause)
	msg := err.Error()
	require.Contains(t, msg, ErrIntegrity.Error())
	require.Contains(t, msg, "content xyz")
	require.Contains(t, msg, "bad bytes")
}
