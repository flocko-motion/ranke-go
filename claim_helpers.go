// package: ranke / claim
// type:    logic
// job:     pure helpers for claim construction — type parsing, pubkey resolution, edge checks
// limits:  no I/O or signing; the builder that calls them is in claim_builder.go
package ranke

import (
	"bytes"
	"crypto"
)

// splitType parses "class/sub" into its two non-empty segments.
func splitType(s string) (class, sub string, err error) {
	if s == "" {
		return "", "", errEmptyType
	}
	slash := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			slash = i
			break
		}
	}
	if slash <= 0 || slash == len(s)-1 {
		return "", "", withDetail(errExpectedClassSub, s)
	}
	return s[:slash], s[slash+1:], nil
}

// matchAll reports whether e satisfies every edge filter (AND). Node-only
// filters are skipped — checking IsEdgeFilter once avoids a MatchEdge call
// per edge for filters that don't apply to edges at all.
func matchAll(e Edge, filters []Filter) bool {
	for _, f := range filters {
		if f.IsEdgeFilter() && !f.MatchEdge(e) {
			return false
		}
	}
	return true
}

// checkSigningConsistency verifies the SigningKey matches the pubkey this
// claim declares (§5.7) and classifies the identity-Sign case (no key, no
// pubkey). It never needs a Universe:
//
//   - Non-root: the contributor's pubkey was resolved to bytes — inline or
//     external — when it was obtained via AsContributor; match on bytes.
//   - Root: this claim declares its own pubkey as its content. Inline bytes
//     are matched directly; an external declaration (ContentHash only) is
//     matched by hashing the key's own pubkey — H(key.Public()) ==
//     ContentHash — so external storage works here too, no fetch required.
func checkSigningConsistency(cfg ClaimBuilder, isRoot bool) error {
	hasSigner := cfg.SigningKey != nil && !isTypedNil(cfg.SigningKey)
	if !isRoot {
		return checkKeyAgainstPubkey(hasSigner, cfg.SigningKey, cfg.Contributor.Pubkey())
	}
	switch {
	case cfg.InlineContent != nil:
		return checkKeyAgainstPubkey(hasSigner, cfg.SigningKey, cfg.InlineContent)
	case cfg.ContentHash != nil:
		// External pubkey declaration — match by hash, no fetch.
		if !hasSigner {
			return errResolvedNoKey
		}
		encoded, err := EncodePublicKey(cfg.SigningKey.Public())
		if err != nil {
			return wrap(errEncodeSigningKey, err)
		}
		h, err := HashContent(encoded)
		if err != nil {
			return err
		}
		if !h.Equal(cfg.ContentHash) {
			return errResolvedMismatch
		}
		return nil
	default:
		// No declared pubkey: identity-Sign (no key), or a key with nothing
		// to attest to (error).
		if hasSigner {
			return errResolvedNoPubkey
		}
		return nil
	}
}

// checkKeyAgainstPubkey matches a signing key against a pubkey given as bytes
// and classifies the missing-half cases: neither → identity Sign; key without
// pubkey or pubkey without key → error; both → the key's public part must
// equal pubkey.
func checkKeyAgainstPubkey(hasSigner bool, key crypto.Signer, pubkey []byte) error {
	hasPubkey := len(pubkey) > 0
	switch {
	case !hasSigner && !hasPubkey:
		return nil // identity-Sign case
	case hasSigner && !hasPubkey:
		return errResolvedNoPubkey
	case !hasSigner && hasPubkey:
		return errResolvedNoKey
	}
	encoded, err := EncodePublicKey(key.Public())
	if err != nil {
		return wrap(errEncodeSigningKey, err)
	}
	if !bytes.Equal(encoded, pubkey) {
		return errResolvedMismatch
	}
	return nil
}

// isTypedNil reports whether i is a nil interface or wraps a nil signer
// (a nil ed25519.PrivateKey would otherwise pass `!= nil`).
func isTypedNil(i any) bool {
	if i == nil {
		return true
	}
	if s, ok := i.(interface{ Public() crypto.PublicKey }); ok {
		// Public() on a nil signer panics; a usable signer yields a pubkey.
		defer func() { _ = recover() }()
		if s.Public() == nil {
			return true
		}
	}
	return false
}

// HeightOf returns the generation number a new claim referencing refs must
// carry: 1 + max(refs' heights), or 0 when refs is empty (an initial node).
// It is the trivial reference computation for ClaimBuilder.Height /
// WithHeight — callers must pass every claim the new one references,
// including its contributor and (for a diff) its predecessor, since those
// edges count toward height too. Tooling at scale caches id→height instead of
// re-walking; this is the uncached, in-memory equivalent.
func HeightOf(refs ...Claim) uint64 {
	var max uint64
	for _, r := range refs {
		if r == nil {
			continue
		}
		if h := r.Node().Height(); h > max {
			max = h
		}
	}
	if len(refs) == 0 {
		return 0
	}
	return max + 1
}

// requiresProvenance reports whether claims of this class need at least
// one derivation/* edge (§3.5).
func requiresProvenance(c NodeClass) bool {
	switch c {
	case NodeClassDerivation, NodeClassEntity, NodeClassRelation:
		return true
	}
	return false
}

// hasDerivationEdge reports whether edges contains a derivation/* edge.
func hasDerivationEdge(edges []*edge) bool {
	for _, e := range edges {
		if e.typeClass == EdgeClassDerivation {
			return true
		}
	}
	return false
}

// asConcreteEdge unwraps an Edge into the concrete *edge. Edge is sealed, so
// this cannot fail on a foreign type — only nil is rejected.
func asConcreteEdge(e Edge) (*edge, error) {
	if e == nil {
		return nil, errNilEdge
	}
	return e.unwrap(), nil
}

// buildContributorEdge constructs the contribution/contributor edge that
// references c.
func buildContributorEdge(c Contributor) (*edge, error) {
	if c == nil {
		return nil, errNilContributor
	}
	return newEdge(EdgeConfig{
		Reference: c.ID(),
		TypeClass: EdgeClassContribution,
		TypeSub:   "contributor",
	})
}
