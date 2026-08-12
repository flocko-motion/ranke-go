// Massive-concurrency conformance: many writers contributing at once, over the
// cross product of Sequencer and storage backend. The property is completeness —
// every write lands, exactly once, whatever order they interleave in.
package tests

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	historymem "github.com/flocko-motion/ranke-go/adapter/history/mem"
	concseq "github.com/flocko-motion/ranke-go/adapter/sequencer/concurrent"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// writersEnv sets how many writers contribute at once. The default keeps the fast
// gate quick; `make test/full` raises it, and a person chasing a race sets it higher
// still — a race that hides at 64 writers shows at 1000.
const writersEnv = "RANKE_CONCURRENCY"

// defaultWriters is what the fast gate pays for. Enough parallelism that an
// unsynchronised head would be caught under -race, small enough that a durable
// backend still finishes in seconds.
const defaultWriters = 64

// serialWriters bounds a Sequencer that takes one contribution at a time. Its count
// is sequential work — each writer waits, then mints its own branch table — so what
// it proves is that nothing is lost across repeated contributions, which a handful
// shows as well as a crowd and far quicker on a durable backend.
const serialWriters = 16

// writers reads the writer count for this run.
func writers(t *testing.T) int {
	t.Helper()
	v := os.Getenv(writersEnv)
	if v == "" {
		return defaultWriters
	}
	n, err := strconv.Atoi(v)
	require.NoErrorf(t, err, "%s=%q is not a number", writersEnv, v)
	require.Positivef(t, n, "%s must be positive", writersEnv)
	return n
}

// tickClock is a monotonic time source safe for concurrent use: writers stamp their
// claims from it while the Sequencer ticks it on its own thread.
type tickClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tickClock) Tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.t
	c.t = c.t.Add(time.Millisecond)
	return out
}

// sequencerRow is one Sequencer under test, paired with the concurrency contract its
// adapter documents. Serialised marks a writer that admits one contribution at a
// time: the caller holds the lock, which is the only correct way to drive it, and the
// completeness property is asserted identically either way. Cap bounds the writer
// count for a row that gains nothing from more (0 = take the run's full count).
type sequencerRow struct {
	Name       string
	Serialised bool
	Cap        int
	New        func(ctx context.Context, u ranke.Universe, hist ranke.History,
		op ranke.Contributor, clk *tickClock) (ranke.Sequencer, error)
}

// sequencerRows is the Sequencer half of the matrix. Raising the writer count is for
// the row that races; the serialised one is capped (see serialWriters).
func sequencerRows() []sequencerRow {
	return []sequencerRow{
		{Name: "concurrent", New: func(ctx context.Context, u ranke.Universe, hist ranke.History,
			op ranke.Contributor, clk *tickClock) (ranke.Sequencer, error) {
			return concseq.NewSequencer(ctx, u, hist, op, clk)
		}},
		{Name: "dev", Serialised: true, Cap: serialWriters,
			New: func(ctx context.Context, u ranke.Universe, hist ranke.History,
				op ranke.Contributor, clk *tickClock) (ranke.Sequencer, error) {
				return devseq.NewSequencer(ctx, u, hist, op, clk)
			}},
	}
}

// TestConcurrentContributionsLoseNothing drives every (Sequencer, storage) pair with
// many writers contributing to one branch at once, and asserts the archive ends up
// holding every token exactly once. Order is a race and does not matter; completeness
// is the contract, and a lost write is the failure this exists to catch.
func TestConcurrentContributionsLoseNothing(t *testing.T) {
	rows, err := backends.Requested()
	require.NoError(t, err)
	n := writers(t)

	for _, be := range rows {
		for _, sr := range sequencerRows() {
			t.Run(be.Name+"/"+sr.Name, func(t *testing.T) {
				u, cleanup, err := be.Open()
				if errors.Is(err, backends.ErrUnavailable) {
					t.Skipf("unavailable here: %v", err)
				}
				require.NoError(t, err)
				defer cleanup()

				rowWriters := n
				if sr.Cap > 0 && sr.Cap < rowWriters {
					rowWriters = sr.Cap
					t.Logf("%d writers (capped from %d: this Sequencer takes one contribution at a time)", rowWriters, n)
				}
				runConcurrentWriters(t, u, sr, rowWriters)
			})
		}
	}
}

// runConcurrentWriters starts n writers against one Sequencer over u and checks what
// the archive holds afterwards.
func runConcurrentWriters(t *testing.T, u ranke.Universe, sr sequencerRow, n int) {
	t.Helper()
	ctx := context.Background()
	clk := &tickClock{t: time.Unix(1_000_000, 0).UTC()}
	op := operatorFor(t, ctx, clk.Tick())
	seq, err := sr.New(ctx, u, historymem.New(), op, clk)
	require.NoError(t, err)

	// One lock for a Sequencer documented as single-threaded; unheld for one that
	// admits parallel writers, so that row races for real.
	var write sync.Mutex

	ids := make([]ranke.Id, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := note(op, clk.Tick(), token(i))
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = c.ID()
			if sr.Serialised {
				write.Lock()
				defer write.Unlock()
			}
			_, errs[i] = helpers.Contribute(ctx, seq, "main", []ranke.Claim{c})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "writer %d", i)
	}
	requireEveryTokenLanded(t, ctx, u, seq, ids, n)
}

// requireEveryTokenLanded is the assertion the test exists for: the branch holds one
// claim per writer, each carrying its own token, none missing and none doubled.
func requireEveryTokenLanded(t *testing.T, ctx context.Context, u ranke.Universe,
	seq ranke.Sequencer, ids []ranke.Id, n int) {
	t.Helper()
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	br, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)

	// Reachability first: a claim the branch cannot reach is a lost write, whatever
	// the store still holds.
	g, err := ranke.NewGraphFromClosure(ctx, br.Head(), u)
	require.NoError(t, err)
	for i, id := range ids {
		ok, err := g.ContainsClaim(ctx, id)
		require.NoErrorf(t, err, "writer %d", i)
		require.Truef(t, ok, "writer %d: %s is not reachable from the branch head", i, id)
	}

	// Then the content, read back through the archive: the tokens are what the
	// writers actually wrote, so a claim present with the wrong bytes still fails.
	seen := map[string]int{}
	for i, id := range ids {
		c, err := arc.GetClaim(ctx, id)
		require.NoErrorf(t, err, "writer %d: load %s", i, id)
		body, err := c.Node().GetInlineContent()
		require.NoErrorf(t, err, "writer %d: content of %s", i, id)
		require.Truef(t, c.Node().ContentComplete(), "writer %d: content served as a prefix", i)
		seen[string(body)]++
	}

	require.Len(t, seen, n, "one distinct token per writer")
	for i := range n {
		require.Equalf(t, 1, seen[token(i)], "token %q must appear exactly once", token(i))
	}
}

// token is writer i's payload — unique per writer, so a lost or duplicated write is
// visible in the set the archive gives back.
func token(i int) string { return fmt.Sprintf("token-%06d", i) }

// note builds the source/note claim one writer contributes. Its only reference is the
// auto-added contributor edge (height 0), so it is height 1 and its closure resolves.
func note(op ranke.Contributor, at time.Time, body string) (ranke.Claim, error) {
	return ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte(body)).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(at).
		Sign()
}

// operatorFor builds the signed root contributor the Sequencer mints and signs with.
func operatorFor(t *testing.T, ctx context.Context, at time.Time) ranke.Contributor {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	require.NoError(t, err)
	op, err := c.AsContributor(ctx, nil, priv)
	require.NoError(t, err)
	return op
}
