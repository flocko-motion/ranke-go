package ranke

// NewMemStore creates an empty in-memory Store. Ephemeral: state is
// lost when the process exits. The cache maps are the source of
// truth — backend is nil, so no persistence path runs.
//
// Use NewFsStore for durable storage.
func NewMemStore() Store {
	return &store{
		claims:   make(map[string]*claim),
		content:  make(map[string][]byte),
		branches: make(map[string]Id),
	}
}
