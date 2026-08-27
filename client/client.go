// package: client / transport
// type:    adapter
// job:     the HTTP client for a running ranke-db — one credential, one base URL, and the request
// plumbing every endpoint call shares, including the server's machine-readable error codes
// limits:  transport only; what the endpoints mean is the OpenAPI contract's and what travels over
// them is the library's (-> codec_wire, query_codec)
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sentinels for the states a caller acts on. The rest arrive as *Error, whose Code
// is the server's stable category.
var (
	// ErrCredentials is both credentials at once, which the endpoint routes on the
	// presented scheme and so cannot resolve.
	ErrCredentials = errors.New("ranke/client: give a token or an API key, not both")
	// ErrNoBaseURL is a client built against nothing.
	ErrNoBaseURL = errors.New("ranke/client: base URL is required")
	// ErrNoUniverse is a contribution of claims naming external content with no
	// Universe to read the bytes from.
	ErrNoUniverse = errors.New("ranke/client: claims name external content, so a Universe is required")
	// ErrContentMissing is a blob a claim addresses that the Universe does not hold —
	// the state that used to travel silently, as a claim with an unbacked hash.
	ErrContentMissing = errors.New("ranke/client: the Universe holds no content under this hash")
	// ErrNilClaim is a nil entry in a contribution.
	ErrNilClaim = errors.New("ranke/client: nil claim")
	// ErrIncompleteLookup is a find naming no type or no field, which would read the
	// whole branch and match on nothing.
	ErrIncompleteLookup = errors.New("ranke/client: a lookup needs at least one type and a field")
)

// Error is a failed request. Code is the server's category — unauthenticated,
// forbidden, not_found, conflict, busy, invalid, unimplemented, internal — and is
// what to branch on; Message is for a person.
type Error struct {
	Status  int
	Code    string
	Message string
}

// Error renders the status and the server's message.
func (e *Error) Error() string {
	return "ranke/client: " + http.StatusText(e.Status) + ": " + e.Message
}

// Category reports whether err is an *Error carrying code.
func Category(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// IsNotFound reports the read that found nothing — for a by-id GET, indistinguishable
// from a claim outside the scope's closure.
func IsNotFound(err error) bool { return Category(err, "not_found") }

// IsUnimplemented reports a route the stack does not mount, which is how a
// production stack answers the dev-only ones.
func IsUnimplemented(err error) bool { return Category(err, "unimplemented") }

// Client talks to one ranke-db instance.
type Client struct {
	base   *url.URL
	http   *http.Client
	token  string
	apiKey string
}

// Option configures a Client.
type Option func(*Client)

// WithToken presents a JWT as `Authorization: Bearer`.
func WithToken(t string) Option { return func(c *Client) { c.token = t } }

// WithAPIKey presents a key as `X-API-Key`.
func WithAPIKey(k string) Option { return func(c *Client) { c.apiKey = k } }

// WithHTTPClient supplies the transport, for timeouts, proxies or a test server.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New returns a client for the instance at baseURL. Presenting both credentials is
// refused here: the endpoint routes on the scheme presented, so two of them name two
// adapters and the request has no single answer.
func New(baseURL string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, ErrNoBaseURL
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	c := &Client{base: u, http: http.DefaultClient}
	for _, o := range opts {
		o(c)
	}
	if c.token != "" && c.apiKey != "" {
		return nil, ErrCredentials
	}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	return c, nil
}

// request is one call's inputs, so the shared plumbing stays one function.
type request struct {
	method string
	path   string // rooted, e.g. "/query"
	body   []byte
	send   string // Content-Type of body
	accept string
}

// do issues the request and returns the response for a 2xx, or an *Error. The body
// is the caller's to close.
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.base.String()+r.path, body)
	if err != nil {
		return nil, err
	}
	if r.send != "" {
		req.Header.Set("Content-Type", r.send)
	}
	if r.accept != "" {
		req.Header.Set("Accept", r.accept)
	}
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.apiKey != "":
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp, nil
}

// readError builds an *Error from the response, falling back to the status when the
// body is not the contract's error shape.
func readError(resp *http.Response) *Error {
	e := &Error{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil || len(b) == 0 {
		return e
	}
	var wire struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &wire) == nil && wire.Code != "" {
		e.Code, e.Message = wire.Code, wire.Error
		return e
	}
	e.Message = string(bytes.TrimSpace(b))
	return e
}

// json issues the request and decodes a JSON response into out.
func (c *Client) json(ctx context.Context, r request, out any) error {
	r.accept = "application/json"
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
