# Test Keys — TEST-ONLY, DO NOT USE FOR ANYTHING REAL

The Ed25519 keypairs in this directory exist solely as **test fixtures** for the Ranke-Graph conformance suite. They are:

- Generated with `openssl genpkey -algorithm ed25519`.
- Checked into a **public git repository**.
- Known to anyone with internet access.

**Never sign anything that matters with these keys.** Never reuse them outside this conformance suite. They are unsafe by construction; their entire point is that any implementation can load them and reproduce the reference outputs.

## Inventory

| Private key   | Public key        | Persona                             | Used in scenarios |
|---------------|-------------------|-------------------------------------|-------------------|
| `alice.pem`   | `alice.pub.pem`   | Alice (initial claim, original key) | 01, 02, 03 |
| `bob.pem`     | `bob.pub.pem`     | Bob (second contributor)            | _reserved for future scenario_ |
| `alice2.pem`  | `alice2.pub.pem`  | Alice (post-rotation key)           | _reserved for future scenario_ |

Charlie has no key — he demonstrates the identity-Sign path (paper §4.1: `Sign(h) = h`; §5.7: empty pubkey → verification trivially succeeds).

`bob.pem` and `alice2.pem` are committed even though no current scenario uses them, because Ed25519 key generation is non-deterministic — regenerating would change every downstream id once the future scenarios land.

## Format

Standard PKCS#8 PEM, Ed25519. Inspect with:

```sh
openssl pkey -in alice.pem -text -noout       # private key
openssl pkey -in alice.pub.pem -pubin -text   # public key
```

Public keys are provided as a convenience for variants that only verify (no signing); they can also be derived from the private key on load.

## Why PEM/PKCS#8

- Standard format, parseable by every mainstream language's stdlib or first-tier crypto library.
- `openssl pkey` round-trips the file, so test setups never depend on a custom parser.
- Demonstrates the realistic "load a key from disk" flow rather than an inline test-only byte literal.
