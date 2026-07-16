// package: ranke / taxonomy
// type:    logic
// job:     list of field names and special values (taxonomy) in context of Universe (storage layer)
// limits:  names only; the tagger logic lives in universe_default_tagger.go,
package ranke

// ReservedPrefix marks the library-owned property namespace: any node/edge
// property key beginning with "_" is reserved (a tag or a codec flag), never a
// user field. A backend that surfaces properties (e.g. neo4j) uses it to
// separate reserved keys from claim fields.
const ReservedPrefix = "_"

// --- Tags: the tagger's mutable per-claim overlay. All keys sit under the "_b"
// family, so a single "_b*" clear scopes exactly to them. ---

const (
	// SpineRevKey records the spine revision a branch-table claim sits at — the
	// commit marker written last per revision.
	SpineRevKey = "_br"
	// BranchTagGlob clears the whole tag family in one SetClaimsTags call.
	BranchTagGlob = "_b*"
)

// BranchTagKey marks a claim as a member of branch b's closure, valued with the
// branch table's height at the revision it entered.
func BranchTagKey(branch string) string { return "_b_" + branch }

// Content location is not a tag or a flag: content_hash present ⟺ external,
// inline content lives in the record (§Content — mutually exclusive), so the
// presence of a content_hash alone signals external. No reserved key needed.
