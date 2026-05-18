# Scenario 02 — Charlie contributes alongside Alice, unsigned

Alice (with key) bootstraps the archive as the initial node, then adds **Charlie** as a second contributor — but Charlie has no key. His `contribution/contributor` claim carries an *empty* pubkey in its content, which activates the identity-Sign path: `Sign(h) = h`, so `id(v) = H(S(v))` for Charlie's claims (paper §4.1, §5.7).

Both signing paths coexist in one graph. The verifier accepts both:
- Alice's signed claims verify via her pubkey + Ed25519 signature check.
- Charlie's unsigned claims trivially pass (empty pubkey → identity Sign → verification is a no-op per §5.7).

The graph stays valid, and integrity for *every* claim is still hash-driven — Charlie loses authenticity, not integrity.

## Inputs

| Path                                       | Role                                    |
|--------------------------------------------|-----------------------------------------|
| `fixtures/keys/alice.pem`                  | Alice's Ed25519 private key             |
| `fixtures/sources/alice_to_bob__apples.eml`| Source ingested by Alice                |
| _(none for Charlie)_                       | Charlie has no key file by design       |

## Steps

1. **Bootstrap Alice as the initial node.**
   Same as scenario 01 step 1 — `contribution/contributor` claim with Alice's pubkey in content, signed by Alice's key.

2. **Add Charlie as an unsigned contributor.**
   Build a `contribution/contributor` claim whose content carries an *empty* pubkey (zero bytes, or an agreed empty marker — exact bytes per #2's design). The claim has a `contribution/contributor` edge pointing to Alice (Alice authorizes Charlie's entry; this edge is signed by Alice). Charlie's claim id itself is computed under identity Sign: `id = H(S(v))`.

3. **Charlie ingests his own source claim.**
   Build a second `source/email` claim — content can be a small distinct note (e.g. a note Charlie added with no email backing). The claim has a `contribution/contributor` edge pointing to **Charlie**, so signing uses Charlie's pubkey lookup → empty → identity Sign → `id = H(S(v))`.

4. **Alice ingests her email** (signed normally, as in scenario 01).

5. **Anchor, persist, dump** — same as scenario 01 steps 3–5.

## Expected outputs

- `./archive/` — the archive containing Alice's signed claims, Charlie's identity-Sign claims, and the head.
- `./ids.txt` — sorted ids; visible from inspection that some ids are pure `H(S(v))` (Charlie's) and others are `Sign(H(S(v)))` (Alice's). The verifier accepts both.

## Verification

- Walk closure, recompute `Sign(H(S(v)))` per claim using the pubkey resolved from each claim's contributor edge (or own content for the initial node).
- For Alice's claims, the signature check uses Ed25519.
- For Charlie's claims, the signature check resolves to identity Sign and trivially passes.
- The archive `Validate()`s.

## Paper references

- §4.1 — Identity Sign choice (`Sign(h) = h`) valid for systems without authenticity needs.
- §5.7 — When contributor's pubkey is empty, identity Sign collapses signing to a no-op; verification trivially succeeds.
