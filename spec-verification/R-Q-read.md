# R-Q* read-language rules — verification

Task sd-7e6652. Rules quoted from `docs/papers/spec/ranke-spec.typ`, read in full
rather than summarised.

Eight rules examined. Nine were verified earlier today and are recorded as SKIPPED
at the end rather than redone.

Method note. Where a verdict turns on "would a test catch this", I removed the
enforcement and ran the suite, then restored it. Those mutations are the evidence
behind every UNTESTED verdict below; the tree is unchanged.

---

### R-QDETAIL — VERIFIED

Rule: "`detail` sets what each element carries: `id`, the id alone, or `claims`, the
claim in full. Under `shape: path` it applies to every claim in the route."

Code: `query.go:153` declares `DetailID` and `DetailClaims` only — the retired
`graph` is gone from the type. `query_codec.go:169` rejects anything else. Both
executors apply it to the whole route: `query_default.go:207` fills `PathId` and
leaves `PathNative` nil under `DetailID`; `adapter/storage/neo4j/query.go:61` nils
`ClaimNative` and `PathNative` together for the same case.

Test: `query_codec_test.go:279` asserts `"graph"` is refused with `ErrQueryEnum`,
naming the retirement. `tests/rql/corpus.go:229,236` cover `detail-id` and
`shape-path-detail-id` across every backend.

---

### R-QENCODING — VERIFIED

Rule: "`encoding` sets the results encoding format: `json`, text with content
base64-encoded, or `cbor`, binary. Both carry the same information."

Code: `query.go:136` declares `ResultJSON` and `ResultCBOR` as the rule names them,
plus `ResultNative` — Go objects for an in-process caller, and the default.
`query_encode.go:27` dispatches on the three; `query_codec.go:175` rejects the rest.

Test: `query_content_test.go` exercises both wire encodings against the content
budget.

Note: `native` is a superset, not a divergence. The rule fixes what the two *wire*
encodings mean; `native` never reaches a wire, and `EncodeResults` returns early for
it (`query_encode.go:29`), leaving the executor's Go objects untouched. Worth stating
because a reader comparing the enum to the rule sees three values where the rule
names two.

---

### R-QEVAL — VERIFIED

Rule: "A query is evaluated in a fixed logical order: (1) `select` generates the
result set, (2) `where` filters it, (3) `order` sorts it, (4) `limit.results`
truncates it, (5) `output` shapes and encodes the result set. […] An engine MAY
reorder or translate these steps, provided the delivered result set is identical to
what this order produces; the native reference engine is the oracle for conformance
testing."

Code: `query_default.go:155` runs the traversal (select), then `finishReached`
applies `evalWhere`, `sortResults`, the `Limit.Results` cut, and `EncodeResults` in
exactly that sequence. `limit.time` and `execution` sit outside it, as the rule says:
the budget bounds the traversal and the report configures logging, neither changing
which claims are selected.

Test: `tests/matrix` is the oracle comparison itself — every backend's answer against
the reference, in stream order.

Note: this rule is the reason two verdicts below are UNTESTED rather than VERIFIED.
Making the reference the oracle means the matrix cannot see a defect the reference
has: the byte-store rows (fs, sqlite, s3, redis) all delegate to `DefaultQuery`, so
they agree with it by construction. Only a native engine — today only neo4j — can
contradict it. That makes neo4j's presence load-bearing for correctness, not just for
coverage, and `make test` runs without it.

---

### R-QFORM — VERIFIED

Rule: "`form` sets which field values a claim carries: `original` as stored, or
`materialized` with its diff chain resolved as `V-DIFF` fixes."

Code: `codec.go:249,325` branch on `FormMaterialized` when encoding, resolving the
overlay; `FormOriginal` emits the stored record. `adapter/storage/stack/query.go:27`
routes a form-original read to a layer holding canonical bytes, since a
structure-only cache cannot answer it.

Test: `tests/rql/corpus.go:241,244` run both forms across every backend;
`adapter/storage/neo4j/routing_test.go:19,43` assert the stack routes by form and
refuses a delta form the layer cannot serve.

---

### R-QLAYER — GAP

Rule: "`execution.layer` pins the query to one named storage or execution layer, and
an empty name MUST be rejected. Absent, the backend chooses by capability. The choice
reaches execution alone: the result set MUST be identical whichever layer serves it
(`R-QEVAL`)."

Code: `query.go:191` declares `Layer string`. `query_codec.go:337,533` carry it
through decode and encode. **Nothing else in the library reads it** — grepping
`Execution.Layer` outside the codec returns nothing, so no query is ever pinned and
`stack` selects by capability regardless of what the caller asked for.

Test: NONE.

Note: three distinct obligations, none met.

1. *Pinning is unimplemented.* A caller naming a layer is silently ignored rather
   than served by that layer or refused. Silent is the bad part: a caller pinning a
   layer to reproduce a bug gets an answer from whichever layer the capability
   routing picked, with nothing saying so.
2. *An empty name is not rejected.* `ValidateQuery` checks `execution.report` but
   never `execution.layer`; I confirmed `Layer: ""` and `Layer: "  "` both validate
   clean, and that the wire form `{"execution":{"layer":""}}` decodes without error.
3. *The type cannot express the rule.* `Layer string` gives one value to "absent"
   and "empty", which the rule distinguishes — absent means choose, empty must be
   rejected. Rejecting `""` at validation would reject every query that simply
   omits the field. A fix therefore needs `*string` (or an explicit presence flag),
   which is a wire-shape change, not a validation one-liner.

Which side should change: the code, on all three. The rule is coherent and the
identical-result-set clause is exactly what a debugging caller needs. But (3) makes
this more than a missing check, so it wants its own task rather than a patch.

---

### R-QSCOPE — VERIFIED

Rule: "`branch` defines the *scope*: the graph within which the query is executed.
Only that graph is read. A branch name confines the query to that branch's closure,
`$archive` to the whole Ranke-Archive, and `$universe` to the closure of the `head`
it requires (`R-QHEAD`). An empty `branch` MUST be rejected. […]"

Code: `query_codec.go:50` returns `ErrQueryNoScope` for an empty branch and
`ErrQueryNoHead` for `$universe` without a head. `archive.go:109` calls
`ValidateQuery` on every read, so a Go-built query meets the same gate as a wire one
— the comment there says so, and it holds. `archive.go:resolveScope` maps the three
scope kinds onto `Scope`, and confinement is enforced per claim in
`query_default.go` (`confine`/`admits`).

Test: `query_confine_test.go` asserts a head outside the branch reads nothing and
that the read is the intersection, on toy graphs with exact answers.
`tests/matrix/branches_test.go` covers branch isolation across backends.

Note: "empty branch MUST be rejected" is met, and unlike R-QLAYER the type poses no
problem — there is no legitimate absent case, since scope is mandatory.

---

### R-QSHAPE — UNTESTED

Rule: "`shape` sets what each result element is: `single`, one reached endpoint per
element, or `path`, a route running outward from the frontier claim its walk began
at. An endpoint reached by more than one route yields exactly one: the shortest, and
where two are equally long the one whose claims sort first on `(created_at, id)`,
compared in order."

Code: `query_walk.go:270` `lessRoute` implements the selection exactly as written —
shorter route first, then claim-by-claim on `(created_at, id)` in order. The code
does **not** predate the decision, contrary to what the task suspected.

Test: NONE for the selection rule.

Note: `tests/matrix/shapes_test.go` covers path shape broadly and
`tests/rql/corpus.go:152` (`output/shape-path-multi-edge`) reaches an endpoint several ways,
but nothing asserts *which* route comes back. I removed the equal-length tie-break —
`lessRoute` returning false for same-length routes — and: the core package passed,
`make test` passed, and only the matrix's neo4j rows failed (4 subtests). So the rule
is enforced, and the only thing standing between a regression and a green run is
neo4j being up.

What would fix the verdict: a toy graph where one endpoint has two equal-length
routes with known `created_at`, asserting the returned route is the earlier one.
That is an exact answer, independent of any second engine.

---

### R-QSORT — UNTESTED

Rule: "`order` is a list of sort keys applied in priority order. Each key names a
`field`, a `compare` […] and a `dir` […]. Claims lacking a key's field sort last. The
archive's natural `(created_at, id)` order […] breaks any remaining ties, and applies
alone when `order` is absent, so the sort MUST always resolve to a total order."

Code: `query_default.go:436` `sortResults` applies the keys in order, returns
`oki` when presence differs so a claim lacking the field sorts last *regardless of
direction*, and falls through to `(created_at, id)`. The Cypher lowering matches:
`adapter/storage/neo4j/query.go:600` emits `<field> IS NULL, <key> <dir>` per key —
nulls last — and `orderLimitClause` appends `created_at, id` unconditionally, so the
tie-break is applied and not merely documented in both engines.

Test: NONE that pins the tie-break.

Note: `query_default_test.go:167` asserts heights are non-decreasing, which says
nothing about the order among equal heights — and the fixture has two claims at
height 1 and two at height 2, so ties exist and go unasserted. I removed the
`(created_at, id)` fall-through from `sortResults` and: the core package passed and
`make test` passed with zero failures; only the matrix's neo4j rows failed (order/
height-desc-limit, height-asc, type-lexical, multi-key).

The exposure is the same as R-QSHAPE and matters more, because the rule names paging
as what depends on the total order: without the tie-break, two pages of the same
query can repeat or skip a claim, and no test in the default gate notices.

What would fix the verdict: a toy graph with two claims tied on the sort key and
known `created_at`, asserting the exact returned sequence.

---

## Summary

| Verdict | Count | Rules |
|---|---|---|
| VERIFIED | 5 | R-QDETAIL, R-QENCODING, R-QEVAL, R-QFORM, R-QSCOPE |
| UNTESTED | 2 | R-QSHAPE, R-QSORT |
| GAP | 1 | R-QLAYER |
| DIVERGENCE | 0 | |
| OUT OF SCOPE | 0 | |

Ranked by what I would fix first.

1. **R-QSORT's missing tie-break test.** Cheapest of the three and guards the
   property the rule itself calls out for paging. A toy graph with a deliberate tie,
   asserting the exact sequence.
2. **R-QSHAPE's missing route-selection test.** Same shape of fix, same reason.
   Together these two close the case where the reference engine's own tie-breaks are
   verified only by a second engine disagreeing.
3. **R-QLAYER.** The largest piece of work and the least urgent: nothing today
   depends on pinning, so it is a missing feature rather than a wrong answer. Needs
   the `*string` decision before anything else, since the empty-versus-absent
   distinction cannot be expressed until then.

A cross-cutting observation, since two findings share it. The reference executor's
tie-breaks — sort and route — are today verified *only* by the neo4j rows
contradicting them. `make test` runs `RANKE_ROWS=mem,fs,sqlite`, all of which
delegate to `DefaultQuery`, so all three agree with a broken reference. Both
mutations above are invisible to the default gate and caught only with neo4j live.
That is R-QEVAL's oracle property working as designed and cutting the wrong way: the
oracle needs exact-answer tests of its own, not just a peer to disagree with it.

## Not checked, and why

- **The nine rules verified earlier today** — R-QCONTENT, R-QCANON, R-QLIMIT,
  R-QSTREAM, R-QREPORT, R-QHEAD, R-QANCHOR, R-QSTEPS, R-QFRONTIER. Skipped as
  instructed, not re-examined; I did not re-derive their verdicts.
- **Whether `json` and `cbor` carry identical information** (R-QENCODING's second
  sentence). I read the dispatch and the round-trip tests, but proving informational
  equivalence means a differential test decoding both and comparing, which does not
  exist. The claim is plausible from the code and unproven by it; I did not raise it
  as a finding because the rule's normative force is on the vocabulary, and calling
  it UNTESTED would overstate what the sentence obliges.
- **Layer routing behaviour under `stack`** beyond confirming nothing reads
  `Execution.Layer`. With the field unused there is no behaviour to test.
- **Neo4j's `null`-ordering semantics against Cypher's own documentation.** I read
  the emitted `IS NULL` term and confirmed it matches the reference's "absent last",
  and the matrix agrees the two produce the same sequence — but I did not verify
  independently that Cypher orders `false` before `true` in every version. The
  matrix passing with neo4j live covers this in practice.
