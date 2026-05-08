package ranke

import (
	"errors"
	"fmt"
	"sort"
)

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
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errors.New("ranke.NewEdge: TypeClass and TypeSub are required")
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
	case EncodingApplication, EncodingAudio, EncodingExample, EncodingFont,
		EncodingImage, EncodingMessage, EncodingModel, EncodingMultipart,
		EncodingText, EncodingVideo:
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
