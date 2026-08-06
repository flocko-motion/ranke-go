// package: ranke / errors
// type:    data
// job:     centralized package error sentinels — one static error per fixed condition
// limits:  no fmt.Errorf anywhere in the package — dynamic errors compose via
// wrap/withDetail/wrapDetail over these sentinels
package ranke

import "errors"

var (
	// --- Universe / storage (exported API) ---
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")
	// ErrUnsupported: the backend does not support this operation (e.g. an
	// opaque byte store asked to tag). Callers gate on the relevant Capability.
	ErrUnsupported = errors.New("ranke: operation not supported by this backend")
	// ErrContentCapped: a layer holds a claim/reference but its stored content
	// is shorter than the expected content_size — capped or truncated (e.g. a
	// cache filled under a smaller cap in a previous run). A stack treats it
	// like a miss and descends to a layer that holds the full bytes.
	ErrContentCapped = errors.New("ranke: content capped (stored shorter than content_size)")
	// ErrBranchNotFound: the archive holds no branch of that name. Also matches
	// ErrNotFound, so a caller may read it either way.
	ErrBranchNotFound = errors.New("ranke.Archive.GetBranch: branch not found")

	// --- Id ---
	errInvalidId     = errors.New("ranke: invalid id")
	errInvalidVarint = errors.New("ranke: invalid varint prefix")

	// --- Type parsing ---
	errEmptyType = errors.New("ranke: empty type")

	// --- Claim / Contributor ---
	errSigningKeyNoPubkey       = errors.New("ranke.Claim.AsContributor: signing key supplied but contributor has no pubkey (identity-Sign contributor)")
	errSigningKeyMismatch       = errors.New("ranke.Claim.AsContributor: signing key does not match contributor pubkey")
	errResolveContributorPubkey = errors.New("ranke.Claim.AsContributor: cannot resolve contributor pubkey from content")
	errNilContributor           = errors.New("ranke: nil contributor")
	errResolvedNoPubkey         = errors.New("ranke: SigningKey supplied but resolved contributor has no pubkey")
	errResolvedNoKey            = errors.New("ranke: resolved contributor has a pubkey but no SigningKey was supplied")
	errResolvedMismatch         = errors.New("ranke: SigningKey's public key does not match the resolved contributor pubkey")

	// --- Node / content ---
	errContentExternal      = errors.New("ranke: content is external — use GetContent with a Universe")
	errNoUniverseForContent = errors.New("ranke: external content requires a Universe")

	// --- Edge ---
	errNilEdge          = errors.New("ranke: nil edge")
	errEdgeRefRequired  = errors.New("ranke.NewEdge: Reference is required")
	errEdgeTypeRequired = errors.New("ranke.NewEdge: Type (or TypeClass + TypeSub) is required")
	errEdgeContentXOR   = errors.New("ranke.NewEdge: inline and external content are mutually exclusive")
	errEdgeRelationDir  = errors.New("ranke.NewEdge: relation/* edges must set RelationDirection (RelationFrom or RelationTo)")

	// --- Claim builder ---
	errClaimTypeRequired        = errors.New("ranke.NewClaim: Type (or TypeClass + TypeSub) is required")
	errClaimContentXOR          = errors.New("ranke.NewClaim: inline and external content are mutually exclusive")
	errClaimContributorRequired = errors.New("ranke.NewClaim: Contributor is required (only the root contribution/contributor may omit it)")
	errEncodingWithoutContent   = errors.New("ranke.NewClaim: encoding set without content")
	errContentWithoutEncoding   = errors.New("content set without encoding: a content-bearing claim or edge must declare a media type")
	errDiffEdgeUnnamed          = errors.New("ranke.NewClaim: a diff claim's edges must be named (except the contributor)")
	errDiffEdgeDupName          = errors.New("ranke.NewClaim: duplicate edge name in a diff claim")
	errTwoContributors          = errors.New("ranke.NewClaim: a claim may carry only one contribution/contributor edge")
	errTwoDiffEdges             = errors.New("ranke.NewClaim: a claim may carry only one contribution/diff edge")
	errHeightRequired           = errors.New("ranke.NewClaim: a claim with references must declare its height (use WithHeight or WithAutoHeight)")
	errHeightOnInitial          = errors.New("ranke.NewClaim: an initial node (no references) must have height 0")
	errHeightWithAuto           = errors.New("ranke.NewClaim: WithHeight and WithAutoHeight are mutually exclusive")

	// --- Graph ---
	errNilClaim          = errors.New("ranke: nil claim")
	errEmptyGraph        = errors.New("ranke.Graph.Consolidate: empty graph")
	errNoContributorEdge = errors.New("ranke: non-initial claim missing contribution/contributor edge")

	// --- Sign ---
	errIdentitySignMismatch = errors.New("ranke.verifySignature: identity Sign mismatch (hash ≠ id)")
	errEd25519Verify        = errors.New("ranke.verifySignature: ed25519 verification failed")

	// --- Verify content ---
	errNilHash = errors.New("ranke: nil content hash")

	// --- Detail-carrying (used with withDetail; the detail is the value) ---
	errNotContributorClaim    = errors.New("ranke.Claim.AsContributor: claim is not contribution/contributor")
	errUnknownNodeClass       = errors.New("ranke.NewClaim: unknown node class")
	errUnknownEncodingClass   = errors.New("ranke.NewClaim: unknown encoding class")
	errUnknownEdgeClass       = errors.New("ranke.NewEdge: unknown edge class")
	errFieldNotSet            = errors.New("ranke: field not set")
	errInvalidFieldName       = errors.New("ranke: invalid field name (use lowercase letters, digits, and '_'; no leading '_')")
	errFieldNameTooLong       = errors.New("ranke: field name too long (max 128 bytes)")
	errFieldValueTooLong      = errors.New("ranke: field value too long (max 64 KiB) — put large data in content")
	errTooManyFields          = errors.New("ranke: too many fields on one record (max 256)")
	errInlineContentTooLarge  = errors.New("ranke: inline content too large (max 1 MiB) — use external content")
	errInvalidSubtype         = errors.New("ranke: invalid subtype (use lowercase letters, digits, and '_'; no leading '_')")
	errInvalidEncodingSubtype = errors.New("ranke: invalid encoding subtype")

	// --- Operation-prefix sentinels (fmt.Errorf replacements) ---
	// Used with wrap/wrapDetail: the sentinel is the operation prefix, the
	// detail carries the stage or a dynamic value, the cause is wrapped.
	errNewClaim              = errors.New("ranke.NewClaim")
	errProvenanceRequired    = errors.New("ranke.NewClaim: this claim class must carry at least one derivation/* edge (§3.5 provenance invariant); type")
	errNewEdge               = errors.New("ranke.NewEdge")
	errRelationDirNonRel     = errors.New("ranke.NewEdge: RelationDirection must be 0 for non-relation edges")
	errBuildGraph            = errors.New("ranke.NewGraphFromClosure")
	errGraphAddClaim         = errors.New("ranke.Graph.AddClaims")
	errConsolidate           = errors.New("ranke.Graph.Consolidate")
	errCopyClaims            = errors.New("ranke.CopyClaims")
	errCopyContents          = errors.New("ranke.CopyContents")
	errQuery                 = errors.New("ranke.Query")
	ErrQueryNoHead           = errors.New("ranke.Query: Select.Head is required under $universe (it has no natural head to scope by)")
	ErrQueryNoScope          = errors.New("ranke.Query: Select.Branch is required (scope is mandatory — use BranchUniverse for an unconfined read)")
	ErrQueryScanShape        = errors.New("ranke.Query: a scan (no Select.Path) reaches claims by no stated route, so Output.Shape must be single")
	ErrQueryScanClaim        = errors.New("ranke.Query: a scan (no Select.Path) has no traversal to start, so Select.Claim is meaningless")
	ErrQueryEncoding         = errors.New("ranke.Query: unknown Output.Encoding (native | json | cbor)")
	errDecodeQuery           = errors.New("ranke.DecodeQuery")
	errEncodeQuery           = errors.New("ranke.EncodeQuery")
	ErrQueryWhereForm        = errors.New("ranke.Query: a where node is exactly one of and | or | not | {field, test}")
	ErrQueryComparisonForm   = errors.New("ranke.Query: a comparison applies exactly one operator (eq | ne | lt | le | gt | ge | in | glob)")
	ErrQueryHops             = errors.New("ranke.Query: a PathStep's hop bounds admit no count")
	ErrQueryEnum             = errors.New("ranke.Query: value outside the set the schema fixes for its field")
	ErrQueryBounds           = errors.New("ranke.Query: value below the minimum the schema fixes for its field")
	ErrReservedType          = errors.New("ranke.Contribution: node type is the Sequencer's alone (lift it to add one)")
	ErrFutureDated           = errors.New("ranke.Contribution: claim is dated after the base the contribution opened against")
	ErrBranchNotCreatable    = errors.New("ranke.Contribution: branch is absent from the base, and creating one is a right of its own")
	ErrUnreadableReference   = errors.New("ranke.Contribution: reference reaches a claim outside the branches this contribution may read")
	errContributionRefs      = errors.New("ranke.AdmitReferences")
	ErrWire                  = errors.New("ranke.Wire")
	ErrWireKind              = errors.New("ranke.Wire: unknown record kind (0 claim | 1 content | 2 branches | 3 referencable | 4 lifted)")
	ErrWireNoBranch          = errors.New("ranke.Wire: a claim record must name the branch it joins")
	ErrWireNoHeader          = errors.New("ranke.Wire: a stream opens with the branches it writes to and the branches it may reference from")
	ErrWireLateConstraint    = errors.New("ranke.Wire: every constraint record precedes the payload")
	ErrWireUndeclared        = errors.New("ranke.Wire: claim names a branch the header does not declare")
	errVerify                = errors.New("ranke.verify")
	ErrKeyNotYetValid        = errors.New("ranke.verify: claim is dated before its contributor key's pubkey_valid_from")
	ErrKeyExpired            = errors.New("ranke.verify: claim is dated after its contributor key's pubkey_expires_after")
	errKeyWindowField        = errors.New("ranke.verify: contributor key validity bound is not RFC 3339")
	errContributorUnresolved = errors.New("ranke.verify: contributor claim unresolved")
	errHeightResolve         = errors.New("ranke.NewClaim: resolve reference height for WithAutoHeight")
	ErrDeleteByNotCopied     = errors.New("ranke.verify: an edge must carry exactly the delete_by its referenced claim declares")
	ErrStructureNotDeletable = errors.New("ranke: a contribution/* claim carries the graph's structure and its own identity, so it takes no delete_by")
	errHeightMismatch        = errors.New("ranke.verify: claim height ≠ 1 + max(reference heights)")
	errNotBranchTable        = errors.New("ranke.Archive.Verify: head is not a contribution/branches claim")
	errRefsBranchTable       = errors.New("ranke.verify: claim references a branch table")
	errEncodeClaim           = errors.New("ranke: encode claim")
	errMarshalCBOR           = errors.New("ranke.MarshalCBOR")
	errDecodeClaim           = errors.New("ranke.DecodeClaim")
	errAssemble              = errors.New("ranke.AssembleClaim")
	errNodePreimage          = errors.New("ranke: claim CBOR has no node preimage")
	errNotArchive            = errors.New("ranke.TagArchive: Archive is not the built-in type")
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
	errNilUniverse = errors.New("ranke.NewArchive: nil Universe")
	errNilHeadID   = errors.New("ranke.NewArchive: nil head id")
	errNilID       = errors.New("ranke.Archive: nil id")
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

// WithDetail returns sentinel with detail appended lazily.
func WithDetail(sentinel error, detail string) error {
	return &wrapErr{sentinel: sentinel, detail: detail}
}

// alsoErr reads as its primary sentinel and matches a second one as well, for a
// condition two rules both name. errors.Join would do the matching and render both
// messages on separate lines; a client shows this one, so the message stays the
// primary's.
type alsoErr struct {
	primary error
	also    error
}

func (e *alsoErr) Error() string   { return e.primary.Error() }
func (e *alsoErr) Unwrap() []error { return []error{e.primary, e.also} }

// alsoMatches returns primary, matchable as also too.
func alsoMatches(primary, also error) error {
	return &alsoErr{primary: primary, also: also}
}

// Wrap returns sentinel wrapping cause; both are matchable via errors.Is.
func Wrap(sentinel, cause error) error {
	return &wrapErr{sentinel: sentinel, cause: cause}
}

// WrapDetail returns sentinel with detail and a wrapped cause.
func WrapDetail(sentinel error, detail string, cause error) error {
	return &wrapErr{sentinel: sentinel, detail: detail, cause: cause}
}
