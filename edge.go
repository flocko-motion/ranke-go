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

// Edge is a directed reference from the owning claim back to the older claim it
// cites (spec §4.2). Part of exactly one claim; may carry inline or external content.
type Edge interface {
	Reference() Id
	Type() string
	TypeClass() EdgeClass
	TypeSub() string

	// Encoding is the content's MIME media type ("class/sub"), "" without content;
	// a content-bearing edge declares one (§Nodes) — reasoning, a proof, a meaning.
	Encoding() string
	EncodingClass() EncodingClass
	EncodingSub() string

	// GetContentHash is the address of EXTERNAL content, H(content); nil for
	// inline content (bytes in the edge, §Content) and for no content.
	GetContentHash() Id
	// GetContentSize is the content's byte length (0 when no content).
	GetContentSize() uint64
	// ContentKind reports whether and where the edge's content lives — Inline,
	// External, or None.
	ContentKind() ContentKind
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

	// unwrap returns the concrete *edge. Being unexported, it seals Edge to this
	// package's *edge, so internal code reaches the concrete type without asserting.
	unwrap() *edge
}

// EdgeConfig is the data-only input to NewEdge. Reference and a type (Type, or
// TypeClass+TypeSub) are required; NewEdge enforces the per-field rules below.
type EdgeConfig struct {
	Reference         Id
	Type              string
	TypeClass         EdgeClass
	TypeSub           string
	Encoding          string // content media type ("class/sub"); required with content, forbidden without
	InlineContent     []byte // exclusive with ContentHash
	ContentHash       Id     // external content, with ContentSize
	ContentSize       uint64
	RelationDirection RelationDirection // RelationFrom or RelationTo on relation/* edges, zero elsewhere
	Fields            map[string]string
}

// edge is the concrete implementation of Edge, immutable after NewEdge.
type edge struct {
	reference         Id
	typeClass         EdgeClass
	typeSub           string
	encodingClass     EncodingClass // "" when no content
	encodingSub       string
	contentHash       Id     // external content address; nil for inline or no content
	content           []byte // inline content bytes; nil when external or none
	contentSize       uint64 // content byte length
	relationDirection RelationDirection
	fields            map[string]string
	id                Id // = H(S(edge))
}

// NewEdge constructs an Edge from cfg (§4.2) — the public wrapper over the
// internal newEdge, which yields the concrete type.
func NewEdge(cfg EdgeConfig) (Edge, error) {
	return newEdge(cfg)
}

// newEdge validates the type and relation_direction, resolves inline vs external
// content, and computes id = H(canonical(edge)), yielding the concrete *edge.
func newEdge(cfg EdgeConfig) (*edge, error) {
	if cfg.Reference == nil {
		return nil, errEdgeRefRequired
	}
	// Type takes precedence over the split form — see EdgeConfig docs.
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return nil, WrapDetail(errNewEdge, "Type", err)
		}
		cfg.TypeClass = EdgeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errEdgeTypeRequired
	}
	if !validEdgeClass(cfg.TypeClass) {
		return nil, WithDetail(errUnknownEdgeClass, string(cfg.TypeClass))
	}
	if err := checkSubtype(cfg.TypeSub); err != nil {
		return nil, err
	}
	if cfg.InlineContent != nil && cfg.ContentHash != nil {
		return nil, errEdgeContentXOR
	}
	if len(cfg.InlineContent) > maxInlineContent {
		return nil, errInlineContentTooLarge
	}
	// Encoding is mandatory with content and forbidden without, as for a node.
	encClass, encSub, err := resolveContentEncoding(cfg.Encoding, cfg.InlineContent != nil || cfg.ContentHash != nil)
	if err != nil {
		return nil, err
	}

	// Relation direction (§4.7): RelationFrom or RelationTo on relation/*, zero elsewhere.
	if cfg.TypeClass == EdgeClassRelation {
		if cfg.RelationDirection != RelationFrom && cfg.RelationDirection != RelationTo {
			return nil, errEdgeRelationDir
		}
	} else {
		if cfg.RelationDirection != 0 {
			return nil, WithDetail(errRelationDirNonRel, string(cfg.TypeClass)+"/*")
		}
	}

	if err := checkFields(cfg.Fields); err != nil {
		return nil, err
	}

	e := &edge{
		reference:         cfg.Reference,
		typeClass:         cfg.TypeClass,
		typeSub:           cfg.TypeSub,
		encodingClass:     encClass,
		encodingSub:       encSub,
		relationDirection: cfg.RelationDirection,
		fields:            cloneFields(cfg.Fields),
	}
	switch {
	case cfg.InlineContent != nil:
		// Inline: the edge id commits to the bytes directly (§Content).
		e.content = cfg.InlineContent
		e.contentSize = uint64(len(cfg.InlineContent))
	case cfg.ContentHash != nil:
		// External: reference content stored elsewhere by hash + size.
		e.contentHash = cfg.ContentHash
		e.contentSize = cfg.ContentSize
	}

	// Compute the edge's own id over its canonical encoding.
	b, err := encodeEdge(e)
	if err != nil {
		return nil, WrapDetail(errNewEdge, "canonical encode", err)
	}
	id, err := hashContent(b)
	if err != nil {
		return nil, WrapDetail(errNewEdge, "hash", err)
	}
	e.id = id
	return e, nil
}

func (e *edge) Reference() Id        { return e.reference }
func (e *edge) Type() string         { return string(e.typeClass) + "/" + e.typeSub }
func (e *edge) TypeClass() EdgeClass { return e.typeClass }
func (e *edge) TypeSub() string      { return e.typeSub }

func (e *edge) Encoding() string {
	if e.encodingClass == "" && e.encodingSub == "" {
		return ""
	}
	return string(e.encodingClass) + "/" + e.encodingSub
}
func (e *edge) EncodingClass() EncodingClass { return e.encodingClass }
func (e *edge) EncodingSub() string          { return e.encodingSub }

// ContentKind derives from the edge's fields: content_hash ⇒ External (§Content:
// mutually exclusive with inline), bytes or a non-zero size ⇒ Inline, else None.
func (e *edge) ContentKind() ContentKind {
	switch {
	case e.contentHash != nil:
		return ContentExternal
	case e.content != nil || e.contentSize > 0:
		return ContentInline
	default:
		return ContentNone
	}
}
func (e *edge) GetContentHash() Id     { return e.contentHash }
func (e *edge) GetContentSize() uint64 { return e.contentSize }

func (e *edge) GetInlineContent() ([]byte, error) {
	if e.ContentKind() == ContentExternal {
		return nil, errContentExternal
	}
	return e.content, nil
}

func (e *edge) GetContent(ctx context.Context, u Universe) (io.Reader, error) {
	if e.content != nil {
		return bytes.NewReader(e.content), nil // inline (§Content)
	}
	if e.contentHash == nil {
		return bytes.NewReader(nil), nil // no content
	}
	if u == nil {
		return nil, errNoUniverseForContent
	}
	return u.StreamContent(ctx, e.contentHash, e.contentSize) // external
}

func (e *edge) RelationDirection() RelationDirection { return e.relationDirection }
func (e *edge) ID() Id                               { return e.id }
func (e *edge) unwrap() *edge                        { return e }

func (e *edge) HasField(name string) bool {
	_, ok := e.fields[name]
	return ok
}

func (e *edge) GetField(name string) (string, error) {
	v, ok := e.fields[name]
	if !ok {
		return "", WithDetail(errFieldNotSet, name)
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
