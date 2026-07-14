package ranke

// some notes on what to unit test regarding claims..

// we'll build different claims in these tests and always apply the same
// verification run: claim -> cbor -> claim -> json -> claim -> cbor -> claim, to then compre
// the initial to the final claim and the first to the second cbor for identity
//

// Test: the most simple claim

// Test: a contributor claim self-signed

// Test: a branch claim

// Test: a claim with inline content in edges and node

// Test: a claim with externalized content in edges and node,
// needs a minimal mem Universe so that content is retrievable

// Test: automatic decision making if content is inlined or not
// (kb threshold)

// Test: user override to store a tiny content external and a big content inline
