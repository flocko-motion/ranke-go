// package: ranke / query
// type:    logic
// job:     fill QueryResult's Encoded fields per Output.Encoding — the stored CBOR verbatim, or the same fields as JSON
// limits:  serialisation only; which claims a read returns is the executor's (-> query_default, adapter/storage/neo4j)
package ranke

// routeIds renders a route's claim ids in order.
func routeIds(route []Claim) []Id {
	if len(route) == 0 {
		return nil
	}
	out := make([]Id, 0, len(route))
	for _, c := range route {
		out = append(out, c.ID())
	}
	return out
}

// EncodeResults fills each result's ClaimEncoded/PathEncoded per out.Encoding.
// Native asks for the Go objects, which the executor already set. A layer holding
// no canonical CBOR fails a cbor read here rather than re-encoding.
func EncodeResults(results []QueryResult, out Output) error {
	switch out.Encoding {
	case "", ResultNative:
		return nil
	case ResultCBOR:
		return encodeCBOR(results)
	case ResultJSON:
		return encodeJSON(results)
	default:
		return WithDetail(ErrQueryEncoding, string(out.Encoding))
	}
}

// encodeCBOR serves each claim as canonical CBOR — the stored record where the
// decode kept it, encoded from the claim's own views otherwise.
func encodeCBOR(results []QueryResult) error {
	return encodeEach(results, func(c Claim) ([]byte, error) { return c.EncodeCBOR() })
}

// encodeJSON converts each parsed claim to JSON — the cheap direction, since the
// traversal already parsed it.
func encodeJSON(results []QueryResult) error {
	return encodeEach(results, func(c Claim) ([]byte, error) { return c.EncodeJSON() })
}

// encodeEach fills every result's ClaimEncoded and PathEncoded through enc.
func encodeEach(results []QueryResult, enc func(Claim) ([]byte, error)) error {
	one := func(c Claim) ([]byte, error) {
		if c == nil {
			return nil, nil
		}
		return enc(c)
	}
	for i := range results {
		b, err := one(results[i].ClaimNative)
		if err != nil {
			return Wrap(errQuery, err)
		}
		if b == nil {
			continue // nothing to encode, so the result keeps the kind it has
		}
		results[i].ClaimEncoded = b
		results[i].Kind = KindClaimEncoded
		if len(results[i].PathNative) == 0 {
			continue
		}
		route := make([][]byte, 0, len(results[i].PathNative))
		for _, pc := range results[i].PathNative {
			pb, err := one(pc)
			if err != nil {
				return Wrap(errQuery, err)
			}
			route = append(route, pb)
		}
		results[i].PathEncoded = route
		results[i].Kind = KindPathEncoded
	}
	return nil
}
