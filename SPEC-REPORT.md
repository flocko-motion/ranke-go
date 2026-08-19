# Spec report — rule/code binding

From sd-924c13, which cited rule ids through verification. Findings in both
directions: checks with no rule, and rules with no check.

A courier, not an artifact: delete it once the spec team has it.

## Corrected in this change — `R-DELBY` is not a rule

Four sites cited `R-DELBY`, which the spec does not define: `verify.go`,
`edge.go` (twice), `contribution.go`. The rule those comments state —
"every edge referencing that claim MUST copy the date, so the gap stays explained
wherever the claim is reached" — is `R-DPLANNED`, word for word. Corrected to
`R-DPLANNED` at all four.

This is the failure mode the task names: an id pointing confidently at nothing. It
survived because nothing checked citations against the spec. `TestCitedRuleIdsExist`
(`spec_citation_test.go`) now does, and fails naming the id and the file.

## Closed — three checks with no rule, now ruled

Reported as checks enforcing invariants the spec did not state. ranke-graph 0.16.2
added a rule for each. Two landed **more specific than the report asked**, so the
verifiers diverged from the new rules in opposite directions; both are corrected
here, each with a test that fires.

**1. Archive head — `V-ARCHIVE`, exact match.** "An archive's head MUST be a
`contribution/branches` claim." That is `ruleArchiveHead` word for word. Cited, no
change.

**2. Branch-table reference — `V-TABLEREF`, the code was looser.** The rule permits a
table to reach a table "only through its `contribution/diff` or
`contribution/branches` edge (`R-C6MERGE`)". `ruleBranchTableReference` exempted the
referencing claim wholesale, so a table reached another through any edge type at all.
The exemption is now those two lineage edges, and the registry statement — which read
only "may be referenced only by another branch-table claim" — says so too.
`TestTableRefThroughOtherEdgeFails` covers it.

**3. Structure not deletable — `R-DSTRUCT`, the code was stricter.** The rule names
four subtypes and says "Any other claim MAY". `CheckDeletable` refused by class, so
every `contribution/*` claim was refused, including an application's own — whose
subtype is open vocabulary (`V-TYPE`). The check is now those four subtypes, and its
comment carries the rule's reasoning for why the set is closed: each is what another
rule reads — a contributor's pubkey (`V-SIG`), the chain to the initial table
(`V-ARCHIVE`), a gap's explanation (`R-DGAP`), a key's window (`R-DEXPIRY`).
`TestOpenContributionSubtypeMayScheduleDeletion` covers it, bounded by
`TestNamedContributionSubtypesRefuseDeletion`.

Neither divergence was reachable from a generated archive — the generator produces
neither a table linked by a derivation edge nor an open `contribution/*` subtype — so
the matrix could not have caught either. That is how both sat unnoticed.

## Finding — a rule with no check

**`V-MONO` is not enforced anywhere.** The rule requires every claim to carry
`created_at` and not to predate what it references:
`created_at(v) ≥ max created_at(u)` over every reference `u`.

No check derives this. `verify.go` reads `created_at` only for the
`WithCreatedAfter` walk bound (`verify.go:249`) and the key-validity window, neither
of which compares a claim against its references. The generator produces monotone
timestamps and several comments mention the rule, but nothing verifies it.

**I think this library owes the check**, not the spec a retraction. `V-MONO` is
FORCED and foundation-paper-derived, it is per-claim and local — the references are
already loaded for `V-HEIGHT`, which walks the same edges — and the closure walk
makes a single-level check transitive, exactly as `verifyHeight` documents for
height. A claim dated before its own source is precisely the kind of thing an
archive exists to make impossible.

Still open, filed as sd-334cf7. Not implemented here: enforcement changes behaviour
and needs its own tests.

## Coverage

Against ranke-graph 0.16.2, which took the `V-*` set from 17 to 19. All 19 were
accounted for. Seventeen of those cited here are still rules:

`V-ALIAS` `V-ARCHIVE` `V-CONTENT` `V-DIFF` `V-DIFFEDGE` `V-HASH` `V-HEIGHT` `V-ID`
`V-REF` `V-REL` `V-ROOT` `V-SER` `V-SIG` `V-SIGN` `V-TABLEREF` `V-TIME` `V-TYPE`

`V-MONO` is the nineteenth, uncited because unenforced — see above.

`V-PROV` was the eighteenth and is **withdrawn**: a derivation edge is a recommended
practice for automatic extraction, not a requirement, and enforcement was removed in
sd-7fe3b2. Named rather than omitted, so a reader who meets the id in an older record
finds out here that it is gone — the failure mode being someone re-adding it as a
missing check. `rule-citations` scans `*.go`, so naming it here breaks no gate.

Two of the eighteen are cited where they are implemented rather than verified, worth
knowing if the citation set is ever read as a verification inventory: `V-ALIAS` at the
alias tables (`field_taxonomy.go`), `V-SER` at the codec (`codec.go`). Both were
already cited before this change.
