package exclusive

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The lock exists to order two OS processes, so a test inside one process cannot
// observe it: a re-entrant flock succeeds against itself. Both cases below therefore
// run a child, which is the only way to see what the lock actually does.

const childEnv = "RANKE_EXCLUSIVE_CHILD"

// TestMain doubles as the child: it takes the lock, reports that it holds it, and
// waits to be killed.
func TestMain(m *testing.M) {
	if name := os.Getenv(childEnv); name != "" {
		release := Lock(name)
		defer release()
		_, _ = os.Stdout.WriteString("held\n")
		time.Sleep(30 * time.Second)
		return
	}
	os.Exit(m.Run())
}

// startHolder runs a child holding name and returns once it reports holding it.
func startHolder(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "TestMain")
	cmd.Env = append(os.Environ(), childEnv+"="+name)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	buf := make([]byte, 5)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := out.Read(buf)
		if n > 0 {
			return cmd
		}
	}
	t.Fatal("the child never reported holding the lock")
	return nil
}

// TestHeldSeesAnotherProcess: the whole point. Without this the lock could be a
// no-op and every caller would still pass.
func TestHeldSeesAnotherProcess(t *testing.T) {
	name := "ranke-exclusive-test-held"
	require.False(t, Held(name), "nothing holds it before the child starts")
	startHolder(t, name)
	require.True(t, Held(name), "the child's lock is visible across processes")
}

// TestLockWaitsForTheHolder: a second taker blocks rather than proceeding, which is
// what keeps two packages from wiping one database.
func TestLockWaitsForTheHolder(t *testing.T) {
	name := "ranke-exclusive-test-wait"
	cmd := startHolder(t, name)

	taken := make(chan struct{})
	go func() {
		release := Lock(name)
		release()
		close(taken)
	}()

	select {
	case <-taken:
		t.Fatal("the lock was taken while another process held it")
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()

	select {
	case <-taken:
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was not released when its holder died")
	}
}

// TestDifferentNamesDoNotBlock: the lock is keyed by service, so unrelated ones stay
// parallel.
func TestDifferentNamesDoNotBlock(t *testing.T) {
	startHolder(t, "ranke-exclusive-test-a")
	done := make(chan struct{})
	go func() {
		release := Lock("ranke-exclusive-test-b")
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an unrelated lock blocked")
	}
}
