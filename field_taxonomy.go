package ranke

import "strings"

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
	// FieldEdgesDiffOmit, on a diff claim, lists the ids of inherited edges
	// to drop when materialising the diff, one id per line (newline-
	// separated — ids never contain a newline). This is the "omit" half of
	// the edge overlay; overwrite/add is done by re-stating edges.
	FieldEdgesDiffOmit = "edges_diff_omit"
	// FieldFieldsDiffOmit is the node-field analogue: newline-separated
	// field names to drop from the inherited set when materialising.
	FieldFieldsDiffOmit = "fields_diff_omit"
)

// splitLines parses a newline-separated list into a set; blank lines and
// surrounding whitespace are ignored. Used for the *_diff_omit fields.
func splitLines(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = struct{}{}
		}
	}
	return out
}

// checkUserFieldName validates a user-supplied field name: non-empty, only
// lowercase letters, digits, and "_", never starting with "_" (that prefix
// — like "." — is a reserved system namespace).
func checkUserFieldName(name string) error {
	if !validFieldChars(name) {
		return withDetail(errInvalidFieldName, name)
	}
	return nil
}

// validFieldChars reports whether name is non-empty, contains only
// [a-z0-9_], and does not start with "_". Node/edge subtypes use the same
// charset (see checkSubtype).
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

// checkSubtype validates a node/edge subtype — the open half of a type.
// Same charset as field names: [a-z0-9] then [a-z0-9_].
func checkSubtype(sub string) error {
	if !validFieldChars(sub) {
		return withDetail(errInvalidSubtype, sub)
	}
	return nil
}

// checkEncodingSubtype validates an encoding (MIME) subtype — more liberal
// than a plain subtype: a leading [A-Za-z0-9], then letters (either case),
// digits, and the real-MIME specials "_", ".", "+", "-".
func checkEncodingSubtype(sub string) error {
	if !validEncodingSubtypeChars(sub) {
		return withDetail(errInvalidEncodingSubtype, sub)
	}
	return nil
}

func validEncodingSubtypeChars(sub string) bool {
	if sub == "" {
		return false
	}
	for i := 0; i < len(sub); i++ {
		c := sub[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !alnum {
				return false // leading char must be alphanumeric
			}
			continue
		}
		if !(alnum || c == '_' || c == '.' || c == '+' || c == '-') {
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
