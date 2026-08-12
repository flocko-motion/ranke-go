// package: ranke / taxonomy
// type:    logic
// job:     `V-TIME` for the optional timestamp fields — delete_by, pubkey_valid_from,
// pubkey_expires_after — refused wherever a claim arrives, by decode and by assemble;
// created_at is a record slot decodeNode already parses
// limits:  no verifyRules entry, since the closure verifier decodes every claim it walks and
// would reach a rule that can never fire; an ABSENT field is no violation, only a
// present unparsable one
package ranke

// timeFields are the optional fields `V-TIME` governs. created_at is not here: it is
// its own record slot, parsed by decodeNode.
var timeFields = []string{FieldDeleteBy, FieldPubkeyValidFrom, FieldPubkeyExpiresAfter}

// checkTimestampFields parses every timestamp field present in fields. Absence is no
// violation — all three are optional — so only a value that is there and will not
// parse is refused.
func checkTimestampFields(fields map[string]string) error {
	for _, name := range timeFields {
		v, ok := fields[name]
		if !ok {
			continue
		}
		if _, err := parseRFC3339Nano(v); err != nil {
			return WithDetail(ErrTimestampForm, name+"="+v)
		}
	}
	return nil
}
