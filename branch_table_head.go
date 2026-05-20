package ranke

import "context"

// BranchTableHead persists B_h (spec §4.7, §4.8) — the single
// mutable Id of the current contribution/branches claim. Lives
// wherever it makes sense for the deployment: a file alongside the
// fs Universe, a row in a database, an S3 object, or memory.
type BranchTableHead interface {
	Load(ctx context.Context) (Id, error) // nil = no branches yet
	Save(ctx context.Context, id Id) error
	Close() error
}
