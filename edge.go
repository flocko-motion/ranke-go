// package: ranke / edge
// type:    data
// job:     the Edge directed-reference type, its filters, and the closed edge type vocabulary
// limits:  does not build claims (-> claim) or serialize edges (-> codec)
package ranke

import (
	"bytes"
	"context"
	"io"
	"sort"
)

// Edge is a directed reference from the owning claim to a prior claim
// (spec §4.2). Each edge is part of exactly one claim. Direction is
// universal: every edge runs from an older claim (its Reference) to
// the newer claim that owns it. Like a node, an edge may carry content —
// inline or external (see the content methods).
type Edge interface {
	Reference() Id
	Type() string
	TypeClass() EdgeClass
	TypeSub() string

	// IsContentExternal reports whether the content is stored as a
	// separate Universe blob (true) or inline in the edge (false).
	IsContentExternal() bool
	// GetContentHash is H(content); nil when the edge carries no content.
	GetContentHash() Id
	// GetContentSize is the content's byte length (0 when no content).
	GetContentSize() uint64
	// GetInlineContent returns the inline content bytes (nil when none);
	// it errors when the content is external.
	GetInlineContent() ([]byte, error)
	// GetContent returns a reader over the content, transparently
	// streaming external content from u; u may be nil for inline content.
	GetContent(ctx context.Context, u Universe) (io.Reader, error)

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
// Content is optional; an edge may carry none. InlineContent holds the
// bytes directly; ContentHash+ContentSize instead reference external
// content (a blob stored elsewhere). InlineContent and ContentHash are
// mutually exclusive — set at most one. RelationDirection must be set
// (RelationFrom or RelationTo) for relation/* edges and left zero
// otherwise; NewEdge enforces this.
type EdgeConfig struct {
	Reference         Id
	Type              string
	TypeClass         EdgeClass
	TypeSub           string
	InlineContent     []byte
	ContentHash       Id
	ContentSize       uint64
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
	contentHash       Id     // H(content); nil when no content
	content           []byte // inline content bytes; nil when external
	contentSize       uint64 // content byte length
	relationDirection RelationDirection
	fields            map[string]string
	id                Id // = H(S(edge))
}

// NewEdge constructs an Edge from the given config (paper §4.2). Validates
// type and relation_direction consistency, resolves inline vs external
// content, and computes the edge's id as H(canonical(edge)).
func NewEdge(cfg EdgeConfig) (Edge, error) {
	if cfg.Reference == nil {
		return nil, errEdgeRefRequired
	}
	// Type takes precedence over the split form — see EdgeConfig docs.
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return nil, wrapDetail(errNewEdge, "Type", err)
		}
		cfg.TypeClass = EdgeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errEdgeTypeRequired
	}
	if !validEdgeClass(cfg.TypeClass) {
		return nil, withDetail(errUnknownEdgeClass, string(cfg.TypeClass))
	}
	if cfg.InlineContent != nil && cfg.ContentHash != nil {
		return nil, errEdgeContentXOR
	}

	// Relation direction rules (§4.7):
	//   - relation/* edges must carry RelationFrom or RelationTo
	//   - non-relation edges must leave it zero
	if cfg.TypeClass == EdgeClassRelation {
		if cfg.RelationDirection != RelationFrom && cfg.RelationDirection != RelationTo {
			return nil, errEdgeRelationDir
		}
	} else {
		if cfg.RelationDirection != 0 {
			return nil, withDetail(errRelationDirNonRel, string(cfg.TypeClass)+"/*")
		}
	}

	for k := range cfg.Fields {
		if err := checkUserFieldName(k); err != nil {
			return nil, err
		}
	}

	e := &edge{
		reference:         cfg.Reference,
		typeClass:         cfg.TypeClass,
		typeSub:           cfg.TypeSub,
		relationDirection: cfg.RelationDirection,
		fields:            cloneFields(cfg.Fields),
	}
	switch {
	case cfg.InlineContent != nil:
		// Inline: hold the bytes, address them by their hash.
		ch, err := hashContent(cfg.InlineContent)
		if err != nil {
			return nil, wrapDetail(errNewEdge, "content hash", err)
		}
		e.content = cfg.InlineContent
		e.contentHash = ch
		e.contentSize = uint64(len(cfg.InlineContent))
	case cfg.ContentHash != nil:
		// External: reference content stored elsewhere by hash + size.
		e.contentHash = cfg.ContentHash
		e.contentSize = cfg.ContentSize
	}

	// Compute the edge's own id over its canonical encoding.
	b, err := encodeEdge(e)
	if err != nil {
		return nil, wrapDetail(errNewEdge, "canonical encode", err)
	}
	id, err := hashContent(b)
	if err != nil {
		return nil, wrapDetail(errNewEdge, "hash", err)
	}
	e.id = id
	return e, nil
}

func (e *edge) Reference() Id        { return e.reference }
func (e *edge) Type() string         { return string(e.typeClass) + "/" + e.typeSub }
func (e *edge) TypeClass() EdgeClass { return e.typeClass }
func (e *edge) TypeSub() string      { return e.typeSub }

func (e *edge) IsContentExternal() bool { return e.content == nil && e.contentHash != nil }
func (e *edge) GetContentHash() Id      { return e.contentHash }
func (e *edge) GetContentSize() uint64  { return e.contentSize }

func (e *edge) GetInlineContent() ([]byte, error) {
	if e.IsContentExternal() {
		return nil, errContentExternal
	}
	return e.content, nil
}

func (e *edge) GetContent(ctx context.Context, u Universe) (io.Reader, error) {
	if e.contentHash == nil {
		return bytes.NewReader(nil), nil // no content
	}
	if e.content != nil {
		return bytes.NewReader(e.content), nil // inline
	}
	if u == nil {
		return nil, errNoUniverseForContent
	}
	return u.StreamContent(ctx, e.contentHash, e.contentSize)
}

func (e *edge) RelationDirection() RelationDirection { return e.relationDirection }
func (e *edge) ID() Id                               { return e.id }

func (e *edge) HasField(name string) bool {
	_, ok := e.fields[name]
	return ok
}

func (e *edge) GetField(name string) (string, error) {
	v, ok := e.fields[name]
	if !ok {
		return "", withDetail(errFieldNotSet, name)
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
	case EdgeClassDerivation, EdgeClassRelation, EdgeClassContribution:
		return true
	}
	return false
}

func validNodeClass(c NodeClass) bool {
	switch c {
	case NodeClassSource, NodeClassDerivation, NodeClassEntity, NodeClassRelation, NodeClassContribution:
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
