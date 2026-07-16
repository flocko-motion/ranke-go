// package: ranke / taxonomy
// type:    logic
// job:     the reserved "_"-prefixed property namespace in one place — the tag keys the tagger owns (spine revision, branch membership) and the codec flags (content-external) — so every layer that surfaces claim properties agrees on them
// limits:  names only; the tagger logic lives in universe_default_tagger.go, the neo4j projection in adapter/storage/neo4j
package ranke

// ReservedPrefix marks the library-owned property namespace: any node/edge
// property key beginning with "_" is reserved (a tag or a codec flag), never a
// user field. A backend that surfaces properties (e.g. neo4j) uses it to
// separate reserved keys from claim fields.
const ReservedPrefix = "_"

// --- Tags: the tagger's mutable per-claim overlay. All keys sit under the "_b"
// family, so a single "_b*" clear scopes exactly to them (and never touches a
// codec flag like _ce). ---

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

// --- Codec flags: reserved ("_" prefix) but NOT tags — excluded from the tag
// overlay. A content-bearing claim/edge carries exactly ONE of these; a
// content_hash with neither flag (or both) is a detectably broken node, so the
// retrieval path never has to guess. ---

const (
	// ContentInternalKey marks a claim's (or edge's) content as inline — held
	// in the claim record, not a separate blob.
	ContentInternalKey = "_ci"
	// ContentExternalKey marks the content as external — a blob elsewhere. A
	// structure cache (neo4j) may hold the content_hash without the bytes, so
	// it records this rather than letting the retrieval path guess.
	ContentExternalKey = "_ce"
)
