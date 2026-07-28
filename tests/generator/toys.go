// package: tests/generator / testkit
// type:    tool
// job:     the toy specs — each the SMALLEST archive exhibiting one corner, so a test asserting on that corner has nothing else in the graph to explain
// limits:  specs only, built by the same builder as the sized graphs (-> generate.go); for breadth at scale use SpecForSize instead
package generator

import "time"

// toyBase is the shared skeleton every toy starts from: one contributor, fixed
// clock, every corner knob off. A toy switches on only what it exhibits.
func toyBase(seed int64) Spec {
	return Spec{
		Seed: seed,
		Size: 1,
		Base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Step: time.Second,

		Contributors:   1,
		TinyBlobBytes:  16,
		LargeBlobBytes: 16,
		MaxEdgeDegree:  1,
	}
}

// ToyDiff is the smallest archive with a content-inheriting diff overlay: one
// source, one base derivation carrying content, one delta over it. The delta
// restates a field only, so its effective content is the base's.
func ToyDiff(seed int64) Spec {
	s := toyBase(seed)
	s.Sources = 1
	s.DiffChainLen = 1
	return s
}

// ToyRelation is the smallest archive with a semantic relation: two entities and
// one relation claim wiring them, carrying both relation_direction polarities.
func ToyRelation(seed int64) Spec {
	s := toyBase(seed)
	s.Sources = 1
	s.Entities = 2
	s.Relations = 1
	return s
}

// ToyExternalContent is the smallest archive whose source content lives in the
// Universe by hash rather than inline — the content a projection layer must serve
// from a durable tier.
func ToyExternalContent(seed int64) Spec {
	s := toyBase(seed)
	s.Sources = 1
	s.ExternalBlobs = 1
	return s
}
