// package: ranke / errors
// type:    data
// job:     centralized package error sentinels — one static error per fixed condition
// limits:  no fmt.Errorf anywhere in the package — dynamic errors compose via wrap/withDetail/wrapDetail over these sentinels
package ranke

import "errors"

var (
	// --- Universe / storage (exported API) ---
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")

	// --- Id ---
	errInvalidId     = errors.New("ranke: invalid id")
	errInvalidVarint = errors.New("ranke: invalid varint prefix")

	// --- Type parsing ---
	errEmptyType = errors.New("ranke: empty type")

	// --- Claim / Contributor ---
	errSigningKeyNoPubkey = errors.New("ranke.Claim.AsContributor: signing key supplied but contributor has no pubkey (identity-Sign contributor)")
	errSigningKeyMismatch = errors.New("ranke.Claim.AsContributor: signing key does not match contributor pubkey")
	errNotBranch          = errors.New("ranke.Claim.AsBranch: claim is not contribution/branch")
	errMissingContributor = errors.New("ranke: missing contributor")
	errNilContributor     = errors.New("ranke: nil contributor")
	errResolvedNoPubkey   = errors.New("ranke: SigningKey supplied but resolved contributor has no pubkey")
	errResolvedNoKey      = errors.New("ranke: resolved contributor has a pubkey but no SigningKey was supplied")
	errResolvedMismatch   = errors.New("ranke: SigningKey's public key does not match the resolved contributor pubkey")
	errForeignClaim       = errors.New("ranke: claim from a foreign implementation")

	// --- Node / content ---
	errContentExternal      = errors.New("ranke: content is external — use GetContent with a Universe")
	errNoUniverseForContent = errors.New("ranke: external content requires a Universe")

	// --- Edge ---
	errNilEdge          = errors.New("ranke: nil edge")
	errForeignEdge      = errors.New("ranke: edge from a foreign implementation")
	errEdgeRefRequired  = errors.New("ranke.NewEdge: Reference is required")
	errEdgeTypeRequired = errors.New("ranke.NewEdge: Type (or TypeClass + TypeSub) is required")
	errEdgeContentXOR   = errors.New("ranke.NewEdge: Content and ContentHash are mutually exclusive")
	errEdgeRelationDir  = errors.New("ranke.NewEdge: relation/* edges must set RelationDirection (RelationFrom or RelationTo)")

	// --- Claim builder ---
	errClaimTypeRequired        = errors.New("ranke.NewClaim: Type (or TypeClass + TypeSub) is required")
	errClaimContentXOR          = errors.New("ranke.NewClaim: Content and ContentHash are mutually exclusive")
	errClaimContributorRequired = errors.New("ranke.NewClaim: Contributor is required (only the root contribution/contributor may omit it)")

	// --- Graph ---
	errNilClaim            = errors.New("ranke: nil claim")
	errRootOnlyNoEdges     = errors.New("ranke: only the root contribution/contributor (set at NewGraph) may have no edges")
	errEmptyGraph          = errors.New("ranke.Graph.Consolidate: empty graph")
	errAlreadyConsolidated = errors.New("ranke.Graph.Consolidate: graph is already consolidated")
	errForeignIdType       = errors.New("ranke: id not a concrete *id (foreign id type)")
	errNoContributorEdge   = errors.New("ranke: non-initial claim missing contribution/contributor edge")

	// --- Sign ---
	errIdentitySignMismatch = errors.New("ranke.verifySignature: identity Sign mismatch (hash ≠ id)")
	errEd25519Verify        = errors.New("ranke.verifySignature: ed25519 verification failed")

	// --- Verify content ---
	errNilHash = errors.New("ranke: nil content hash")

	// --- Detail-carrying (used with withDetail; the detail is the value) ---
	errNotContributorClaim  = errors.New("ranke.Claim.AsContributor: claim is not contribution/contributor")
	errUnknownNodeClass     = errors.New("ranke.NewClaim: unknown node class")
	errUnknownEncodingClass = errors.New("ranke.NewClaim: unknown encoding class")
	errUnknownEdgeClass     = errors.New("ranke.NewEdge: unknown edge class")
	errFieldNotSet          = errors.New("ranke: field not set")

	// --- Operation-prefix sentinels (fmt.Errorf replacements) ---
	// Used with wrap/wrapDetail: the sentinel is the operation prefix, the
	// detail carries the stage or a dynamic value, the cause is wrapped.
	errNewClaim              = errors.New("ranke.NewClaim")
	errProvenanceRequired    = errors.New("ranke.NewClaim: this claim class must carry at least one derivation/* edge (§3.5 provenance invariant); type")
	errNewEdge               = errors.New("ranke.NewEdge")
	errRelationDirNonRel     = errors.New("ranke.NewEdge: RelationDirection must be 0 for non-relation edges")
	errBuildGraph            = errors.New("ranke.NewGraphFromClosure")
	errGraphAddClaim         = errors.New("ranke.Graph.AddClaims")
	errUnknownRefClaim       = errors.New("ranke.Graph: edge references unknown claim (atomic creation rule §4.3)")
	errRefMissingClaim       = errors.New("ranke.Graph: edge references missing claim")
	errConsolidate           = errors.New("ranke.Graph.Consolidate")
	errGraphVerifyStage      = errors.New("ranke.Graph.Verify")
	errContributorNotInGraph = errors.New("ranke.Graph: contributor claim not in graph")
	errEncodeClaim           = errors.New("ranke: encode claim")
	errDecodeClaim           = errors.New("ranke.DecodeClaim")
	errID                    = errors.New("ranke.Id")
	errEncodePubkey          = errors.New("ranke: unsupported public key type")
	errDecodePubkey          = errors.New("ranke.DecodePublicKey")
	errSignHash              = errors.New("ranke.signHash")
	errVerifySig             = errors.New("ranke.verifySignature")
	errLoadKeypair           = errors.New("ranke.LoadPrivateKey")
	errLoadPrivKey           = errors.New("ranke.LoadEd25519PrivateKeyPEM")
	errLoadPubKey            = errors.New("ranke.LoadEd25519PublicKeyPEM")
	errVerifyContentOp       = errors.New("ranke.VerifyContent")
	errVerifyingReader       = errors.New("ranke.NewVerifyingReader")
	errContent               = errors.New("ranke: content")
	errExpectedClassSub      = errors.New("ranke: expected \"class/sub\"")
	errEncodeSigningKey      = errors.New("ranke: encode signing key's pubkey")
	errArchiveLoadHead       = errors.New("ranke.NewArchive: load head")

	// --- Archive ---
	errNilUniverse     = errors.New("ranke.NewArchive: nil Universe")
	errNilHeadID       = errors.New("ranke.NewArchive: nil head id")
	errHeadNotFound    = errors.New("ranke.NewArchive: head claim not found")
	errNilID           = errors.New("ranke.Archive: nil id")
	errBranchNotFound  = errors.New("ranke.Archive.GetBranch: branch not found")
	errBranchCollision = errors.New("ranke.Archive.GetBranch: name collision — multiple branches match")
	errVerifyTODO      = errors.New("ranke: verification not implemented")
)

// wrapErr attaches an optional detail string and/or an optional cause to a
// sentinel without building any string at construction — the message is
// composed lazily in Error(). errors.Is/As match the sentinel and, when
// present, the cause (via Unwrap). It replaces fmt.Errorf across the
// package: withDetail for %s/%q/%d-style detail, wrap for %w-style cause,
// wrapDetail for both.
type wrapErr struct {
	sentinel error
	detail   string
	cause    error
}

func (e *wrapErr) Error() string {
	msg := e.sentinel.Error()
	if e.detail != "" {
		msg += ": " + e.detail
	}
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

// Unwrap exposes the sentinel and, when present, the cause, so errors.Is
// and errors.As match either.
func (e *wrapErr) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

// withDetail returns sentinel with detail appended lazily.
func withDetail(sentinel error, detail string) error {
	return &wrapErr{sentinel: sentinel, detail: detail}
}

// wrap returns sentinel wrapping cause; both are matchable via errors.Is.
func wrap(sentinel, cause error) error {
	return &wrapErr{sentinel: sentinel, cause: cause}
}

// wrapDetail returns sentinel with detail and a wrapped cause.
func wrapDetail(sentinel error, detail string, cause error) error {
	return &wrapErr{sentinel: sentinel, detail: detail, cause: cause}
}
