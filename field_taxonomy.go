// package: ranke / taxonomy
// type:    logic
// job:     field-name/value size caps and the well-known field-name constants, enforced at
// claim/edge construction
// limits:  reference-impl caps (the paper leaves them open); the codec/verifier never reject an
// already-stored record (-> claim_builder)
package ranke

import (
	"strconv"
	"strings"
)

// Field-size caps for the reference implementation; fields are metadata, so
// large data belongs in content. Enforced by NewClaim/NewEdge.
const (
	maxFieldNameLen    = 128       // bytes, one field name
	maxFieldValueLen   = 64 * 1024 // bytes, one field value
	maxFieldsPerRecord = 256       // fields on one node or edge
)

// Field is a node/edge field name: open user vocabulary over [a-z0-9_] with no
// leading "_". Charsets outside that are reserved system namespaces, e.g. ".".
type Field string

// Aliases are bare; the codec adds the reserved "." prefix on the wire.
const (
	FieldName             = "name"
	FieldNameAlias        = "n"
	FieldEdges            = "edges"
	FieldEdgesAlias       = "e"
	FieldContent          = "content"
	FieldContentAlias     = "c"
	FieldContentSize      = "content_size"
	FieldContentSizeAlias = "s"
	FieldContentHash      = "content_hash"
	FieldContentHashAlias = "h"
	FieldHeight           = "height"
	FieldHeightAlias      = "H"
	// FieldEdgesDiffOmit lists, on a diff claim, names of inherited edges to drop when
	// materialising, one per line — only named edges inherit. Overwrite/add is
	// re-stating the edge.
	FieldEdgesDiffOmit      = "edges_diff_omit"
	FieldEdgesDiffOmitAlias = "E"
	// FieldFieldsDiffOmit is the node-field analogue: newline-separated names.
	FieldFieldsDiffOmit      = "fields_diff_omit"
	FieldFieldsDiffOmitAlias = "F"
	// FieldPubkeyValidFrom and FieldPubkeyExpiresAfter bound a contributor key's
	// validity (RFC 3339, paper 2 §Contributor Keys): a claim it signed is dated
	// within the closed window they describe. Either may stand alone.
	FieldPubkeyValidFrom         = "pubkey_valid_from"
	FieldPubkeyValidFromAlias    = "v"
	FieldPubkeyExpiresAfter      = "pubkey_expires_after"
	FieldPubkeyExpiresAfterAlias = "x"
	// FieldDeleteBy schedules a claim's bytes for removal (RFC 3339, paper 2
	// §Deletion). Every edge referencing such a claim carries the date too, so the
	// schedule travels with the reference and explains the gap the deletion leaves.
	FieldDeleteBy      = "delete_by"
	FieldDeleteByAlias = "d"
)

// splitLines parses a newline-separated list (a *_diff_omit field) into a set.
func splitLines(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = struct{}{}
		}
	}
	return out
}

// checkUserFieldName validates a user-supplied field name: non-empty, at most
// maxFieldNameLen bytes, [a-z0-9_], no leading "_" (a reserved namespace).
func checkUserFieldName(name string) error {
	if len(name) > maxFieldNameLen {
		return errFieldNameTooLong // don't echo a possibly-huge name
	}
	if !validFieldChars(name) {
		return WithDetail(errInvalidFieldName, name)
	}
	return nil
}

// checkFields validates a whole node/edge field set at construction: at most
// maxFieldsPerRecord entries, each name valid, each value within the cap.
func checkFields(fields map[string]string) error {
	if len(fields) > maxFieldsPerRecord {
		return WithDetail(errTooManyFields, strconv.Itoa(len(fields)))
	}
	for k, v := range fields {
		if err := checkUserFieldName(k); err != nil {
			return err
		}
		if len(v) > maxFieldValueLen {
			return WithDetail(errFieldValueTooLong, k)
		}
	}
	return nil
}

// validFieldChars reports whether name is non-empty, is [a-z0-9_], and has no
// leading "_". Node/edge subtypes share the charset (see checkSubtype).
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

// checkSubtype validates a node/edge subtype: [a-z0-9] then [a-z0-9_].
func checkSubtype(sub string) error {
	if !validFieldChars(sub) {
		return WithDetail(errInvalidSubtype, sub)
	}
	return nil
}

// checkEncodingSubtype validates an encoding (MIME) subtype: a leading
// [A-Za-z0-9], then alphanumerics and the MIME specials "_", ".", "+", "-".
func checkEncodingSubtype(sub string) error {
	if !validEncodingSubtypeChars(sub) {
		return WithDetail(errInvalidEncodingSubtype, sub)
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

// fieldNameToAlias maps a well-known field name to its bare alias (`V-ALIAS`); user
// names pass through.
func fieldNameToAlias(n Field) Field {
	switch n {
	case FieldName:
		return FieldNameAlias
	case FieldEdges:
		return FieldEdgesAlias
	case FieldContent:
		return FieldContentAlias
	case FieldContentSize:
		return FieldContentSizeAlias
	case FieldContentHash:
		return FieldContentHashAlias
	case FieldHeight:
		return FieldHeightAlias
	case FieldEdgesDiffOmit:
		return FieldEdgesDiffOmitAlias
	case FieldFieldsDiffOmit:
		return FieldFieldsDiffOmitAlias
	case FieldDeleteBy:
		return FieldDeleteByAlias
	case FieldPubkeyValidFrom:
		return FieldPubkeyValidFromAlias
	case FieldPubkeyExpiresAfter:
		return FieldPubkeyExpiresAfterAlias
	default:
		return n
	}
}

// fieldNameFromAlias maps a bare alias back to its canonical field name.
func fieldNameFromAlias(c Field) Field {
	switch c {
	case FieldNameAlias:
		return FieldName
	case FieldEdgesAlias:
		return FieldEdges
	case FieldContentAlias:
		return FieldContent
	case FieldContentSizeAlias:
		return FieldContentSize
	case FieldContentHashAlias:
		return FieldContentHash
	case FieldHeightAlias:
		return FieldHeight
	case FieldEdgesDiffOmitAlias:
		return FieldEdgesDiffOmit
	case FieldFieldsDiffOmitAlias:
		return FieldFieldsDiffOmit
	case FieldDeleteByAlias:
		return FieldDeleteBy
	case FieldPubkeyValidFromAlias:
		return FieldPubkeyValidFrom
	case FieldPubkeyExpiresAfterAlias:
		return FieldPubkeyExpiresAfter
	default:
		return c
	}
}
