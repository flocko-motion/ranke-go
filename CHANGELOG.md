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

### Changed

- `dev.NewSequencer` and `concurrent.NewSequencer` take the bookmark store from the
  Universe instead of a parameter: `NewSequencer(ctx, u, self, clock)`. Both refuse
  a Universe reporting `Capabilities.Bookmarks` false at construction, joining
  `ErrUnsupported` so the refusal stays matchable.

### Removed

- `NewMemoryBookmarks` and `fs.NewBookmarks`, and `storage.NewBlobBookmarks` is
  unexported. A bookmark store now comes from the Universe holding it and nowhere
  else, so a detached 𝒰_hist — a file bookmark list over an in-memory universe —
  cannot be expressed. Configuration carrying an independent history section has
  nothing left to point at.
