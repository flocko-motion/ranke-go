package ranke

// Field is a node/edge field name. Field names are open user vocabulary,
// constrained to a safe charset (enforced by NewClaim/NewEdge): lowercase
// letters, digits, and "_", never starting with "_". Everything outside
// that charset is a reserved namespace the system owns — the "." prefix
// (used for the wire aliases below) today, with room for more prefixes
// later. Names like "name" are ordinary fields; the structural properties
// (content, edges, …) live in their own slots, not the field map, so they
// never collide with a user field of the same name.
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

// reservedFieldNames are plain (charset-valid) field names the system
// owns — they name structural node/edge properties, so users may not set
// them in Fields. The system sets them through dedicated builder inputs
// (e.g. ClaimBuilder.Name), never through the Fields map.
var reservedFieldNames = map[string]struct{}{
	FieldName:        {},
	FieldEdges:       {},
	FieldContent:     {},
	FieldContentSize: {},
	FieldContentHash: {},
}

// checkUserFieldName validates a user-supplied field name. Valid names are
// non-empty, use only lowercase letters, digits, and "_", never start with
// "_" (that prefix — like "." — is a reserved system namespace), and are
// not one of the reserved structural names.
func checkUserFieldName(name string) error {
	if !validFieldChars(name) {
		return withDetail(errInvalidFieldName, name)
	}
	if _, reserved := reservedFieldNames[name]; reserved {
		return withDetail(errReservedFieldName, name)
	}
	return nil
}

// validFieldChars reports whether name is non-empty, contains only
// [a-z0-9_], and does not start with "_".
func validFieldChars(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			// allowed anywhere
		case c == '_':
			if i == 0 {
				return false // no leading underscore
			}
		default:
			return false
		}
	}
	return true
}

// fieldNameToAlias / fieldNameFromAlias convert between a canonical field
// name and its "." alias for wire compression (wired up by the codec).
func fieldNameToAlias(n Field) Field {
	switch n {
	default:
		return n
	}
}

func fieldNameFromAlias(c Field) Field {
	return c
}
