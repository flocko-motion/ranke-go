# Scenario 03 — Agent extracts knowledge from sources

Alice (initial node, signed) ingests two emails. An **extraction agent** — its own `contribution/contributor` claim, attributed to Alice (i.e. its contribution chain leads back to Alice as the operator) — then derives entities, relations, and a summary from those sources. Every derived claim cites its inputs via `derivation/source` edges (§3.5).

This scenario exercises the **semantic side** of the data structure: the agent-as-contributor pattern, derivation chains, `entity/*` nodes, and `relation/*` nodes with `relation/from` and `relation/to` edges. Two Bobs from two different emails get distinct ids by content-addressing — the structural reading stays acyclic even though _Alice — knows → Bob_ paired with _Bob — ignores → Alice_ forms a cycle in the **semantic reading** (paper §4.7, §5.8).

## Inputs

| Path                                       | Role                                                |
|--------------------------------------------|-----------------------------------------------------|
| `fixtures/keys/alice.pem`                  | Alice's Ed25519 private key                         |
| `fixtures/sources/alice_to_bob__apples.eml`| First email — Alice tells Bob she likes apples      |
| `fixtures/sources/alice_to_bob__family.eml`| Second email — Alice asks Bob to greet Bob Jr.      |

The extraction agent is also signed — its key is Alice's (in this scenario the agent is one of Alice's tools; she takes responsibility for what it emits). In a richer setup the agent could have its own key; that's a future scenario.

## Steps

1. **Bootstrap Alice as the initial node** (as in scenario 01).

2. **Add the extraction agent as a contributor.**
   Build a `contribution/contributor` claim representing the agent — content describes the agent (e.g. `extraction-agent-v1`). The `contribution/contributor` edge points to Alice (the agent's contribution is attributed to Alice). Signed by Alice's key.

3. **Ingest both emails** as `source/email` claims (each attributed to Alice).

4. **Agent emits a summary derivation.**
   `derivation/summary` claim with text condensing the apples email. Has a `derivation/source` edge to the apples source. Attributed to the agent.

5. **Agent extracts entities.** For each entity (Alice, Bob (sr.), Bob Jr., apples):
   - Build an `entity/*` claim (`entity/person` or `entity/object`) with the entity label as content.
   - Add a `derivation/source` edge to the source the entity was extracted from.
   - Attributed to the agent.

   Note: Bob (sr.) extracted from the apples email and Bob Jr. extracted from the family email have **distinct ids** even though both contain "Bob" — content includes the source reference.

6. **Agent emits semantic relations.**
   - `relation/likes` — Alice → apples (sourced from apples email).
   - `relation/knows` — Alice → Bob sr. (sourced from apples email).
   - `relation/ignores` — Bob sr. → Alice (sourced from apples email; an inferred low-conviction claim).
   - `relation/family` — symmetric between Bob sr. and Bob Jr. (sourced from family email).

   Each relation claim has:
   - One `derivation/source` edge per source it draws from.
   - One `relation/<sub>` edge per from-side entity (`RelationFrom`).
   - One `relation/<sub>` edge per to-side entity (`RelationTo`).
   - Symmetric relations (`family`) use all-`RelationFrom` edges (paper §4.7).

7. **Anchor, persist, dump** — same as scenario 01 steps 3–5.

## Expected outputs

- `./archive/` — Ranke-Archive containing Alice, agent, two source emails, summary, four entities, four relations, and the head.
- `./ids.txt` — sorted ids of all of the above.

## Verification

- Walk closure, recompute and check signatures throughout.
- Confirm the two `Bob` entities have distinct ids (content-addressing across distinct sources).
- Confirm the structural reading (raw edges) is acyclic even though the semantic reading admits the Alice↔Bob cycle.
- The archive `Validate()`s.

## Paper references

- §3.5 — Levels of distillation; every derived claim cites its inputs.
- §4.7 — Relations as reified claims; `relation_direction` field with `from = 1`, `to = -1`; symmetric relations use all-`from`.
- §5.8 — Semantic Relations: structural vs. semantic reading; structural is acyclic, semantic admits cycles.
