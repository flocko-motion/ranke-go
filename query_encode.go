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
// out.Form, inlining the content out.Content allows (`R-QCONTENT`). Native asks for the
// Go objects, which the executor already set and which keep their content whole — an
// in-process caller holds the claim itself, so a cap would only cost it the bytes.
func EncodeResults(results []QueryResult, out Output) error {
	var enc func(*claim, Form, *contentBudget) ([]byte, error)
	switch out.Encoding {
	case "", ResultNative:
		return nil
	case ResultCBOR:
		enc = (*claim).encodeCBOR
	case ResultJSON:
		enc = (*claim).encodeJSON
	default:
		return WithDetail(ErrQueryEncoding, string(out.Encoding))
	}
	return encodeEach(results, func(c Claim) ([]byte, error) {
		cc := c.unwrap()
		if cc == nil {
			return nil, nil
		}
		return enc(cc, out.Form, newContentBudget(out.Content))
	})
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
		if results[i].Kind == KindReport {
			continue // a report is not a claim, and keeps the tag it travels under
		}
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
