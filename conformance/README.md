# Conformance Suite

This directory is the **cross-implementation conformance suite** for the Ranke-Graph. The Go implementation in this repository is the **reference**; any variant implementation (e.g. the planned Python port) is conformant if and only if it produces the same outputs as the reference for the same inputs.

## Layout

```
conformance/
├── README.md                                 # this file
├── .gitattributes                            # pins fixture bytes (no CRLF mangling)
├── fixtures/
│   ├── keys/                                 # global — shared across scenarios
│   │   ├── README.md
│   │   ├── alice.pem  / alice.pub.pem        # Ed25519 PKCS#8 (test-only)
│   │   ├── alice2.pem / alice2.pub.pem       # Alice's post-rotation key
│   │   └── bob.pem    / bob.pub.pem          # Bob's signing key
│   └── sources/                              # global — shared across scenarios
│       ├── alice_to_bob__apples.eml          # paper §1 email
│       └── alice_to_bob__family.eml          # second email used by scenario 03
└── scenarios/
    ├── 01_alice_signs_a_source/
    │   ├── scenario.md                       # human-readable description
    │   ├── main.go                           # standalone runnable program
    │   ├── ids.txt                           # sorted claim ids (output)
    │   └── archive/                          # full Ranke-Archive on disk (output)
    ├── 02_charlie_unsigned_alongside/
    └── 03_agent_extracts_knowledge/
```

## Scenarios are standalone programs

Each scenario is a small **runnable Go program** that uses `ranke-go` as a library — not a unit test, and not a thin wrapper around test helpers. To run a scenario:

```sh
go run ./conformance/scenarios/01_alice_signs_a_source
```

The program loads its inputs from `conformance/fixtures/`, builds the graph through the public API, and writes `ids.txt` and `archive/` into its own directory. Reading `main.go` shows how a real consumer of `ranke-go` would call the library.

Python's port lives in a sibling repo; each scenario there is a parallel `main.py` doing the same steps with the same fixture inputs. Symmetric form across impls makes step-by-step diffing trivial.

## How a variant verifies conformance

For each scenario:

1. **Read inputs from `fixtures/`** — load the same PEM keys and `.eml` sources the Go reference loads.
2. **Run the scenario** — implemented in the variant's language, following the numbered steps in `scenario.md`.
3. **Check conformance, choose your depth:**
   - **Quick:** sort your own resulting claim ids and diff against `scenarios/<n>/ids.txt`.
   - **Thorough:** compare your produced Ranke-Archive byte-for-byte against `scenarios/<n>/archive/`. This verifies serialization, not just hashing.

A divergence at the `ids.txt` level points to a hash, signing, or serialization bug. A divergence only at the `archive/` level points to an on-disk layout bug (the ids match but the variant lays bytes out differently).

## Why Go is the reference

The Go implementation is authoritative. The expected outputs in `ids.txt` and `archive/` are produced *by* Go (by running `main.go`) and checked into the repo. A variant cannot "fix" the reference by updating these files; if a divergence reveals a bug in Go, the Go impl is fixed first, the expected files are regenerated, and variants catch up.

## Distribution

The whole `conformance/` directory is bundled as `ranke-conformance-vX.Y.Z.tar.gz` and attached to each GitHub release (see issue #8). Variants pin a version, download via HTTPS, and run.

## Status

This scaffold ships the directory layout, the fixture inputs, scenario descriptions, and compilable-stub `main.go` files. The `ids.txt` and `archive/` contents per scenario are populated once the Sign primitive (#1), Ed25519 reference impl (#2), and verification path (#3) have landed, and the scenario programs do real work. Until then those files are marked **TBD** and `main.go` prints a not-yet-implemented message.

## Relation to `tests/`

`tests/` holds classic Go unit tests — fast, granular, exercising individual functions and error paths.

`conformance/scenarios/` holds end-to-end usage programs — they tell a story, demonstrate the public API, and produce the byte-exact artifacts variants are graded against. They complement, not duplicate, the unit tests.
