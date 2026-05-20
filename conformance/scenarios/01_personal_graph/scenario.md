# Scenario 01 — Alice signs a source

Alice contributor claim with a Ed25519 pubkey is the **initial node** of a fresh Ranke-Archive.
She then ingests a set of emails as `source/email` claims, signed by her key. She creates
derivations extracting knowledge from those emails, resulting in a small knowledge graph.

This is the foundational scenario: it exercises archive creation, signing, adding sources,
creating derivations, building a semantic graph.
