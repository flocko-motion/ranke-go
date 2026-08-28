// package: stack / persistence
// type:    logic
// job:     the background filler — a bounded, deduplicating, lossy batch queue for opportunistic
// cache writes that must never slow or fail a read
// limits:  puts claims into layers the stack chose; deciding WHICH layers and WHY is the router's
// (-> stack.go, query.go)
package stack

import (
	"context"
	"sync"

	"github.com/rankegraph/ranke-go"
)

const (
	fillMaxBytes = 8 << 20 // held content awaiting a flush
	fillMaxJobs  = 512     // guard for many tiny claims
)

// Healing observes the background filler. A stack satisfies it, so a caller that
// needs a fill to have landed — a test, or a server pacing its own work — can ask.
type Healing interface {
	// HealIdle reports that every fill offered so far has reached its layer.
	HealIdle() bool
}

// HealIdle reports that the background filler has nothing left to write.
func (s *stack) HealIdle() bool { return s.heal.idle() }

// fillJob is one content blob to write into one layer.
type fillJob struct {
	layer int
	blob  *ranke.ContentBlob
}

// filler writes claims into layers off the read path: an inbox map takes new jobs
// (dedup for free) while a worker drains the batch it swapped out. Drops when full.
type filler struct {
	mu      sync.Mutex
	inbox   map[string]fillJob
	bytes   int
	dropped int
	writing bool // a batch is in flight, so idle stays false until it lands

	doorbell chan struct{} // 1-buffered: a pending ring needs no second
	stop     chan struct{}
	done     chan struct{}
}

func newFiller(put func(context.Context, []fillJob)) *filler {
	f := &filler{
		inbox:    map[string]fillJob{},
		doorbell: make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go f.run(put)
	return f
}

// offerBlob queues content for layer li — the read-through that brings a layer up
// to its own cap, off the path that served the read. Never blocks, never fails.
func (f *filler) offerBlob(ctx context.Context, li int, b ranke.ContentBlob) {
	if b.Hash == nil {
		return
	}
	f.queue(ctx, li, b.Hash.String(), len(b.Content), fillJob{layer: li, blob: &b})
}

// queue admits one job under the size and count caps, dropping it when either is
// reached — a fill only makes a later read faster.
func (f *filler) queue(ctx context.Context, li int, id string, size int, job fillJob) {
	if f == nil {
		return
	}
	key := id + ":" + itoa(li)

	f.mu.Lock()
	switch {
	case f.inbox[key].blob != nil: // already queued — the dedup the map gives free
	case len(f.inbox) >= fillMaxJobs || f.bytes+size > fillMaxBytes:
		f.dropped++
		dropped := f.dropped
		f.mu.Unlock()
		ranke.ReportEvent(ctx, "stack", "fill-dropped", ranke.ReportDebug, "",
			map[string]any{"layer": li, "total": dropped})
		return
	default:
		f.inbox[key] = job
		f.bytes += size
	}
	f.mu.Unlock()

	select { // ring the doorbell unless it is already ringing
	case f.doorbell <- struct{}{}:
	default:
	}
}

// run takes the whole inbox, writes it, and waits for the next ring.
func (f *filler) run(put func(context.Context, []fillJob)) {
	defer close(f.done)
	for {
		select {
		case <-f.stop:
			return
		case <-f.doorbell:
		}
		batch := f.take()
		if len(batch) > 0 {
			put(context.WithoutCancel(context.Background()), batch)
		}
		f.mu.Lock()
		f.writing = false
		f.mu.Unlock()
	}
}

// take swaps the inbox out, leaving producers a fresh one, and marks the batch
// in flight so idle stays false until it lands.
func (f *filler) take() []fillJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inbox) == 0 {
		return nil
	}
	batch := make([]fillJob, 0, len(f.inbox))
	for _, j := range f.inbox {
		batch = append(batch, j)
	}
	f.inbox = map[string]fillJob{}
	f.bytes = 0
	f.writing = true
	return batch
}

// idle reports that nothing is queued and no batch is in flight, so every fill
// offered so far has reached its layer.
func (f *filler) idle() bool {
	if f == nil {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inbox) == 0 && !f.writing
}

// close stops the worker and abandons whatever is pending.
func (f *filler) close() {
	if f == nil {
		return
	}
	close(f.stop)
	<-f.done
}

// writeBatch groups a batch by layer and writes each group in one call,
// discarding failures.
func (s *stack) writeBatch(ctx context.Context, batch []fillJob) {
	byLayer := map[int][]ranke.ContentBlob{}
	for _, j := range batch {
		if j.blob != nil {
			byLayer[j.layer] = append(byLayer[j.layer], *j.blob)
		}
	}
	for li, bs := range byLayer {
		_ = s.layers[li].u.PutContents(ctx, bs)
	}
}

// itoa avoids a strconv import for a one-digit-ish layer index.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
