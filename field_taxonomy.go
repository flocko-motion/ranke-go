package ranke

// EdgeClass is the closed top-level vocabulary for edge types.
type Field string

const (
	FieldName             = "name"
	FieldNameAlias        = ".n"
	FieldEdges            = "edges"
	FieldEdgesAlias       = ".e"
	FieldContent          = "content"
	FieldContentAlias     = ".c"
	FieldContentSize      = "content_size"
	FieldContentSizeAlias = ".s"
	FieldContentHash      = "content_hash"
	FieldContentHashAlias = ".h"
)

func fieldNameToAlias(n Field) Field {
	switch n {
	default:
		return n
	}
}

func fieldNameFromAlias(c Field) Field {
	return c
}
