package ranke

// NewMemArchive creates an empty in-memory Archive. Ephemeral: state is
// lost when the process exits. The cache maps are the source of
// truth — backend is nil, so no persistence path runs.
//
// Use NewFsArchive for durable storage.
func NewMemArchive() Archive {
	return &archive{
		claims:  make(map[string]*claim),
		content: make(map[string][]byte),
		// branchesHead nil: archive starts with no branches.
	}
}
