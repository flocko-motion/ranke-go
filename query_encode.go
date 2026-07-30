// package: ranke / query
// type:    logic
// job:     fill QueryResult's Encoded fields per Output.Encoding — the stored CBOR verbatim, or the
// same fields as JSON
// limits:  serialisation only; which claims a read returns is the executor's (-> query_default,
// adapter/storage/neo4j)
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

// EncodeResults fills each result's ClaimEncoded/PathEncoded per out.Encoding, in
// out.Form. Native asks for the Go objects, which the executor already set.
func EncodeResults(results []QueryResult, out Output) error {
	switch out.Encoding {
	case "", ResultNative:
		return nil
	case ResultCBOR:
		return encodeEach(results, func(c Claim) ([]byte, error) { return c.EncodeCBOR(out.Form) })
	case ResultJSON:
		return encodeEach(results, func(c Claim) ([]byte, error) { return c.EncodeJSON(out.Form) })
	default:
		return WithDetail(ErrQueryEncoding, string(out.Encoding))
	}
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
