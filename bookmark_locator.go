// package: ranke / bookmark_locator
// type:    logic
// job:     BookmarkLocator — which bookmark list to open, given as a seed or as the id of one
// entry, so the seed is settled before any cursor over the list exists
// limits:  names a list and resolves it; the cursor and its searches are bookmarks.go's
// (-> bookmarks, bookmark_store)
package ranke

import (
	"bytes"
	"context"
)

// BookmarkLocator names one bookmark list. Its two arms carry different contracts,
// and both deliver the seed before the cursor exists — which is what keeps a list's
// key material immutable for the life of a Bookmarks.
type BookmarkLocator struct {
	seed []byte
	id   Id
}

// Seed locates the list keyed on s, which starts at index 0 and is never pruned, so
// its genesis is detectable by probing id_seq(0, s). Any non-empty s serves: it keeps
// lists apart and is no security value, though a minted one carries 128 bits
// (`V-BMENV`).
func Seed(s []byte) BookmarkLocator { return BookmarkLocator{seed: bytes.Clone(s)} }

// At locates a PRUNED list from the id of any surviving entry, whose verified record
// yields the seed every bookmark carries — so index 0 need not exist (foundation
// paper §Backup). Pruning is out-of-band, BookmarkStore having no Delete.
func At(id Id) BookmarkLocator { return BookmarkLocator{id: id} }

// Open resolves the locator against the Universe holding the list's 𝒰_hist.
func (l BookmarkLocator) Open(ctx context.Context, u Universe) (*Bookmarks, error) {
	if l.id != nil {
		return OpenBookmarks(ctx, u, l.id)
	}
	return NewBookmarks(u, l.seed)
}
