// package: ranke / edge
// type:    data
// job:     the Edge directed-reference type, its filters, and the closed edge type vocabulary
// limits:  does not build claims (-> claim) or serialize edges (-> serialize)
package ranke

import (
	"errors"
	"fmt"
	"sort"
)

// Filter selects a subset of edges matching some criterion. Passed
// to Claim.Edges as a variadic list — every filter must match (AND).
// Callers can implement their own; NewTypeFilter and NewEncodingFilter
// cover the common cases.
type Filter interface {
	Match(e Edge) bool
}

// Edge is a directed reference from the owning claim to a prior claim
// (spec §4.2). Each edge is part of exactly one claim. Direction is
// universal: every edge runs from an older claim (its Reference) to
// the newer claim that owns it.
type Edge interface {
	Reference() Id
	Type() string
	TypeClass() EdgeClass
	TypeSub() string
	// Content is inline (paper §4.2 simplified schema) — no separate
	// content_hash for edges.
	Content() []byte
	// RelationDirection is RelationFrom (+1) or RelationTo (-1) on
	// relation/* edges, 0 elsewhere (§4.7).
	RelationDirection() RelationDirection
	HasField(name string) bool
	GetField(name string) (string, error)
	Fields() []string
	ID() Id
}

// EdgeConfig is the data-only input to NewEdge.
//
// Required: Reference plus a type (either Type or TypeClass+TypeSub).
// Content is the edge's inline content (paper §4.2 simplified schema).
// RelationDirection must be set (RelationFrom or RelationTo) for
// relation/* edges and left zero otherwise; NewEdge enforces this.
type EdgeConfig struct {
	Reference         Id
	Type              string
	TypeClass         EdgeClass
	TypeSub           string
	Content           []byte
	RelationDirection RelationDirection
	Fields            map[string]string
}

// edge is the concrete implementation of Edge. Created via NewEdge and
// immutable after. id is computed once at construction from the
// canonical serialization of the other fields.
type edge struct {
	reference         Id
	typeClass         EdgeClass
	typeSub           string
	content           []byte
	relationDirection RelationDirection
	fields            map[string]string
	id                Id // = H(S(edge))
}

// NewEdge constructs an Edge from the given config (paper §4.2
// simplified schema). Validates type and relation_direction
// consistency; computes the edge's id as H(canonical(edge)).
//
// Edges have inline content — Content bytes travel directly with
// the edge in the canonical encoding; there is no separate
// content_hash for edges.
func NewEdge(cfg EdgeConfig) (Edge, error) {
	if cfg.Reference == nil {
		return nil, errors.New("ranke.NewEdge: Reference is required")
	}
	// Type takes precedence over the split form — see EdgeConfig docs.
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewEdge: Type: %w", err)
		}
		cfg.TypeClass = EdgeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errors.New("ranke.NewEdge: Type (or TypeClass + TypeSub) is required")
	}
	if !validEdgeClass(cfg.TypeClass) {
		return nil, fmt.Errorf("ranke.NewEdge: unknown EdgeClass %q", cfg.TypeClass)
	}

	// Relation direction rules (§4.7):
	//   - relation/* edges must carry RelationFrom or RelationTo
	//   - non-relation edges must leave it zero
	if cfg.TypeClass == EdgeRelation {
		if cfg.RelationDirection != RelationFrom && cfg.RelationDirection != RelationTo {
			return nil, errors.New("ranke.NewEdge: relation/* edges must set RelationDirection (RelationFrom or RelationTo)")
		}
	} else {
		if cfg.RelationDirection != 0 {
			return nil, fmt.Errorf("ranke.NewEdge: RelationDirection must be 0 for non-relation edges (%s/*)", cfg.TypeClass)
		}
	}

	e := &edge{
		reference:         cfg.Reference,
		typeClass:         cfg.TypeClass,
		typeSub:           cfg.TypeSub,
		content:           cfg.Content,
		relationDirection: cfg.RelationDirection,
		fields:            cloneFields(cfg.Fields),
	}

	// Compute the edge's own id over the canonical encoding (which
	// includes content inline).
	bytes, err := encodeEdge(e)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewEdge: canonical encode: %w", err)
	}
	id, err := hashContent(bytes)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewEdge: hash: %w", err)
	}
	e.id = id
	return e, nil
}

func (e *edge) Reference() Id                        { return e.reference }
func (e *edge) Type() string                         { return string(e.typeClass) + "/" + e.typeSub }
func (e *edge) TypeClass() EdgeClass                 { return e.typeClass }
func (e *edge) TypeSub() string                      { return e.typeSub }
func (e *edge) Content() []byte                      { return e.content }
func (e *edge) RelationDirection() RelationDirection { return e.relationDirection }
func (e *edge) ID() Id                               { return e.id }

func (e *edge) HasField(name string) bool {
	_, ok := e.fields[name]
	return ok
}

func (e *edge) GetField(name string) (string, error) {
	v, ok := e.fields[name]
	if !ok {
		return "", fmt.Errorf("ranke.Edge.GetField: %q not set", name)
	}
	return v, nil
}

func (e *edge) Fields() []string {
	names := make([]string, 0, len(e.fields))
	for k := range e.fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// --- helpers ---

func validEdgeClass(c EdgeClass) bool {
	switch c {
	case EdgeDerivation, EdgeRelation, EdgeContribution:
		return true
	}
	return false
}

func validNodeClass(c NodeClass) bool {
	switch c {
	case NodeSource, NodeDerivation, NodeEntity, NodeRelation, NodeContribution:
		return true
	}
	return false
}

func validEncodingClass(c EncodingClass) bool {
	switch c {
	case encApplication, encAudio, encExample, encFont,
		encImage, encMessage, encModel, encMultipart,
		encText, encVideo:
		return true
	}
	return false
}

func cloneFields(f map[string]string) map[string]string {
	if len(f) == 0 {
		return nil
	}
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

// --- Edge type vocabulary (spec §4.8) ---

// EdgeClass is the closed top-level vocabulary for edge types.
type EdgeClass string

const (
	EdgeDerivation   EdgeClass = "derivation"
	EdgeRelation     EdgeClass = "relation"
	EdgeContribution EdgeClass = "contribution"
)

// Closed contribution/* edge type strings, for EdgeConfig.Type. Branch
// and Prune are edge-only — no claim counterpart.
const (
	EdgeContributor = string(EdgeContribution) + "/contributor"
	EdgeHead        = string(EdgeContribution) + "/head"
	EdgeBranches    = string(EdgeContribution) + "/branches"
	EdgeBranch      = string(EdgeContribution) + "/branch"
	EdgePrune       = string(EdgeContribution) + "/prune"
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7):
// zero = not a relation edge, RelationFrom (+1) / RelationTo (-1)
// otherwise. All-from or all-to expresses a symmetric relation.
type RelationDirection int8

const (
	RelationFrom RelationDirection = 1
	RelationTo   RelationDirection = -1
)
