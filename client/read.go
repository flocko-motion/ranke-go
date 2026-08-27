// package: client / transport
// type:    adapter
// job:     the reads — POST /query in either framing, the cacheable by-id GETs, and /health with
// the wait a caller would otherwise spend on a guessed sleep
// limits:  transport only; the read language is the library's (-> query_codec) and a claim's bytes
// decode there too (-> codec)
package client

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// Scope names which of the three by-id routes a read takes. A branch confines to its
// closure; the other two are the reserved scopes.
type Scope string

// The reserved scopes. Any other value is a branch name.
const (
	ScopeArchive  Scope = "$archive"
	ScopeUniverse Scope = "$universe"
)

// ErrAmbiguous is a find that matched more than one claim where one was asked for.
// Choosing between them is the caller's, so this refuses rather than guessing.
var ErrAmbiguous = errors.New("ranke/client: more than one claim matches")

// path returns the by-id route for this scope.
func (s Scope) path(id string) string {
	switch s {
	case ScopeArchive:
		return "/archive/claims/" + url.PathEscape(id)
	case ScopeUniverse:
		return "/universe/claims/" + url.PathEscape(id)
	default:
		return "/branches/" + url.PathEscape(string(s)) + "/claims/" + url.PathEscape(id)
	}
}

// Health is the stack's liveness and the identity it merges under.
type Health struct {
	Status string `json:"status"`
	Signer string `json:"signer"`
}

// Health reports the instance as up, or the reason it is not.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	h := &Health{}
	if err := c.json(ctx, request{method: "GET", path: "/health"}, h); err != nil {
		return nil, err
	}
	return h, nil
}

// WaitReady polls /health until it answers or within elapses, so a caller starting a
// stack waits for the fact rather than for a guessed interval.
func (c *Client) WaitReady(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	const gap = 50 * time.Millisecond
	var last error
	for {
		if _, err := c.Health(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Add(gap).Before(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gap):
		}
	}
}

// Query runs q and returns the records of the result sequence, undecoded, in the
// framing the response declares. The final record is the QueryReport when
// Execution.Report asked for one.
func (c *Client) Query(ctx context.Context, q ranke.Query) ([][]byte, error) {
	body, err := ranke.EncodeQuery(q)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{
		method: "POST", path: "/query",
		body: body, send: "application/json",
		accept: MediaJSONSeq + ", " + MediaCBORSeq,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return splitBody(resp)
}

// splitBody picks the framing from the response's media type, so a caller never has
// to agree with the server twice about what output.encoding asked for.
func splitBody(resp *http.Response) ([][]byte, error) {
	ct, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		ct = strings.TrimSpace(resp.Header.Get("Content-Type"))
	}
	switch ct {
	case MediaJSONSeq:
		return splitJSONSeq(resp.Body)
	case MediaCBORSeq:
		return splitCBORSeq(resp.Body)
	default:
		return nil, ranke.WithDetail(ErrUnknownFraming, ct)
	}
}

// QueryClaims runs q shaped so every record is a claim's stored envelope, and decodes
// each one. The shaping is fixed here — original form, no inlined content, CBOR — as
// that is the combination whose bytes re-hash to the id (`R-QCANON`); any other is a
// rendering, and would decode to a claim whose id does not hold.
func (c *Client) QueryClaims(ctx context.Context, q ranke.Query) ([]ranke.Claim, error) {
	q.Output.Detail = ranke.DetailClaims
	q.Output.Form = ranke.FormOriginal
	q.Output.Encoding = ranke.ResultCBOR
	q.Output.Content = &ranke.OutputContent{Max: 0}
	q.Execution.Report = ""

	records, err := c.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	claims := make([]ranke.Claim, 0, len(records))
	for _, rec := range records {
		cl, err := decodeStored(rec)
		if err != nil {
			return nil, err
		}
		claims = append(claims, cl)
	}
	return claims, nil
}

// GetClaim fetches one claim by id within scope. A claim outside that scope's closure
// is absent, which the endpoint answers the same as one that never existed.
func (c *Client) GetClaim(ctx context.Context, scope Scope, id ranke.Id) (ranke.Claim, error) {
	if id == nil {
		return nil, ErrNilClaim
	}
	resp, err := c.do(ctx, request{method: "GET", path: scope.path(id.String()), accept: "application/cbor"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ranke.DecodeClaim(id, raw)
}

// decodeStored decodes a stored envelope, deriving the id it must carry: id(v) is the
// hash of exactly these bytes, so a record answers for its own name and no separate
// id has to travel beside it.
func decodeStored(raw []byte) (ranke.Claim, error) {
	id, err := ranke.HashContent(raw)
	if err != nil {
		return nil, err
	}
	return ranke.DecodeClaim(id, raw)
}
