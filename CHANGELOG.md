# Changelog

What each release changed for someone depending on this repository.

## Unreleased

### Added

- `Universe.Bookmarks() BookmarkStore` — a backend's 𝒰_hist, the second address
  scheme keyed on `id_seq(i, s)`. Every `Universe` implementation must answer it.
  The Universe owning the store is what lets a bookmark list inherit the layering,
  replication and backup of the storage beneath it.
- `Capabilities.Bookmarks` — whether a backend can hold an archive's locator. True
  for every in-tree adapter but neo4j, which is a projection you drop and reindex.
  A `stack` reports it when an authoritative layer has it; a `partition` when every
  shard does, the list being replicated to all of them.
- `UnsupportedBookmarks()` — the store a Universe reporting `Bookmarks` false hands
  out, answering `ErrUnsupported`.
- `BookmarkLocator`, with two arms carrying two contracts: `Seed(s)` for a list that
  starts at index 0 and is never pruned, and `At(id)` for a pruned one, opened from a
  surviving entry whose record yields the seed. `Open` resolves either against a
  Universe.
- `MintSeed()` — a fresh 128-bit list seed (`V-BMENV`), for whoever founds a list and
  keeps the value.

### Changed

- `dev.NewSequencer` and `concurrent.NewSequencer` take a `BookmarkLocator` and read
  the store off the Universe: `NewSequencer(ctx, u, loc, self, clock)`. Both refuse a
  Universe reporting `Capabilities.Bookmarks` false at construction, joining
  `ErrUnsupported` so the refusal stays matchable. `dev` no longer derives a seed of
  its own, so a reproducible run states the one it wants.
- `NewBookmarks(u, seed)` and `OpenBookmarks(ctx, u, id)` name a Universe rather than
  a store, which is what stops a caller pairing one Universe's 𝒰_hist with another.
  `NewBookmarks` now reports an error, refusing an empty seed.
- A bookmark list's seed, locator and entry index are fixed at construction. `Append`
  mints nothing and writes none of them, so `Seed()` and `BookmarkId()` answer the
  same value from any goroutine and never answer nil.

### Removed

- `NewMemoryBookmarks` and `fs.NewBookmarks`, and `storage.NewBlobBookmarks` is
  unexported. A bookmark store now comes from the Universe holding it and nowhere
  else, so a detached 𝒰_hist — a file bookmark list over an in-memory universe —
  cannot be expressed. Configuration carrying an independent history section has
  nothing left to point at.
