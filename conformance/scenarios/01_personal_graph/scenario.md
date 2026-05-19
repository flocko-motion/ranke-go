# Scenario 01 — Alice signs a source

Alice contributor claim with a Ed25519 pubkey is the **initial node** of a fresh Ranke-Archive.
She then ingests a set of emails as `source/email` claims, signed by her key. She creates
derivations extracting knowledge from those emails, resulting in a small knowledge graph.

This is the foundational scenario: it exercises archive creation, signing, adding sources,
creating derivations, building a semantic graph.

## Inputs

| Path                                       | Role                              |
|--------------------------------------------|-----------------------------------|
| `fixtures/keys/alice.pem`                  | Alice's Ed25519 private key       |
| `fixtures/sources/alice_to_bob__apples.eml`| The email Alice ingests           |

## Steps

TODO: write the steps. Don't be too verbose, keep it bullet style - the code itself is the verbosity.

1. **Persist to `./archive/`.**
   Write the archive to the scenario's own `archive/` directory using the standard fs layout.

2. **Dump `./ids.txt`.**
   Walk every claim in U, sort their ids lexicographically, write one id per line.

## Expected outputs

- `./archive/` — the full Ranke-Archive on disk (variants compare byte-for-byte for thorough conformance).
- `./ids.txt` — sorted claim ids (variants compare line-for-line for quick conformance).

## Verification

The reference Go run also verifies, at the end:

- Every claim's id matches `Sign(H(S(v)))` recomputed from disk.
- The closure from `B_h` reaches Alice's contributor claim.
- The archive `Validate()`s.

## Paper references

- §4.1 — `Sign` primitive and the identity `id(v) = Sign(H(S(v)))`.
- §4.3 — Initial node (no-edge claim).
- §4.5 — Universe `U` as a set.
- §4.9 — Branch table and the `contribution/branches` / `contribution/branch` shape.
- §5.7 — Identity and Authenticity; pubkey lookup for initial vs. non-initial nodes.
