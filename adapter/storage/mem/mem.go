// package: mem / persistence
// type:    adapter
// job:     ephemeral in-memory Universe for tests and short-lived sessions — a thin alias to the
// core reference implementation
// limits:  nothing persists across process restarts (-> adapter/fs)
//
// Package mem is an ephemeral in-memory persistence adapter for a ranke
// Universe. It is now a thin alias over the core reference implementation
// (ranke.NewMemoryUniverse) — which holds live claim objects in maps and
// delegates closure/copy to the core Default* helpers — kept as an adapter
// entry point so existing `adapter/storage/mem` imports keep working.
package mem

import "github.com/rankegraph/ranke-go"

// New returns an ephemeral, in-memory Universe (ranke.NewMemoryUniverse).
func New() ranke.Universe { return ranke.NewMemoryUniverse() }
