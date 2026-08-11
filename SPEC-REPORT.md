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

## Findings — checks with no rule

Three checks in `verifyRules` (`verify.go:385`) enforce invariants the spec does not
state. Left uncited and unchanged.

**1. Branch-table reference layering** — `ruleBranchTableReference`, `verify.go:469`.
Enforces: a `contribution/branches` claim may be referenced only by another
branch-table claim. Nothing in the spec says this. `R-C6MERGE` is adjacent — every
branch table holds its predecessor in provenance — but it constrains what a table
references, not what may reference a table. **I think the spec owes a rule.** The
check protects a real property: without it any claim could reference a branch table
and pull the archive spine into an ordinary closure, which is a layering violation a
reader of the spec alone would not know to avoid.

**2. Archive head is a branch table** — `ruleArchiveHead`, `verify.go:529`.
Enforces: an archive's head claim is `contribution/branches`. `R-C6MERGE` implies it
("the chain from the archive's head reaches the initial table unbroken") without
stating it as a verification rule. **I think the spec owes a rule**, or `R-C6MERGE`
should say it outright — the implication is only visible to someone who already
knows the answer.

**3. Structure takes no delete_by** — `ruleStructureNotDeletable`, `verify.go:519`.
Enforces: a `contribution/*` claim carries no `delete_by`. The deletion rules
(`R-DPLANNED`, `R-DREQUEST`, `R-DGAP`) say nothing about which classes may be
scheduled for deletion. **I think the spec owes a rule.** The property matters:
deleting a contributor or a branch table would remove the record its edges depend
on, so exempting `contribution/*` is what keeps a scheduled deletion from taking
the structure with it.

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

Not added here: this task cites rules, and adding enforcement would change
behaviour and need its own tests.

## Coverage

All 17 `V-*` rules are accounted for. Sixteen are cited in the code:

`V-ALIAS` `V-CONTENT` `V-DIFF` `V-DIFFEDGE` `V-HASH` `V-HEIGHT` `V-ID` `V-PROV`
`V-REF` `V-REL` `V-ROOT` `V-SER` `V-SIG` `V-SIGN` `V-TIME` `V-TYPE`

`V-MONO` is the seventeenth, uncited because unenforced — see above.

Two of the sixteen are cited where they are implemented rather than verified, which
is worth knowing if the citation set is ever read as a verification inventory:
`V-ALIAS` at the alias tables (`field_taxonomy.go`), `V-SER` at the codec
(`codec.go`). Both were already cited before this change.
