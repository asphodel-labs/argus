// Package gh holds the GitHub GraphQL and REST calls of argus.
// It uses the local gh authentication through go-gh.
package gh

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Client sends GraphQL and REST requests to GitHub.
type Client struct {
	gql  *api.GraphQLClient
	rest *api.RESTClient
}

// New returns a client for opts. Empty fields of opts come from the gh
// configuration, so api.ClientOptions{} uses the local gh login.
// Tests set Host, AuthToken, and Transport.
func New(opts api.ClientOptions) (*Client, error) {
	inner := opts.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	opts.Transport = headerTransport{inner: inner}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, fmt.Errorf("make GraphQL client: %w", err)
	}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("make REST client: %w", err)
	}
	return &Client{gql: gql, rest: rest}, nil
}

// RateLimit is the GraphQL rate limit after a query.
type RateLimit struct {
	Cost      int
	Remaining int
	ResetAt   time.Time
}

// query sends one GraphQL query and decodes the data into resp.
func (c *Client) query(ctx context.Context, name, query string, vars map[string]any, resp any) error {
	if err := c.gql.DoWithContext(ctx, query, vars, resp); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// headersKey is the context key of the request headers that headerTransport
// adds to one request.
type headersKey struct{}

// withHeaders returns a context that makes headerTransport add h to the
// request. The go-gh REST client has no headers for each request.
func withHeaders(ctx context.Context, h http.Header) context.Context {
	return context.WithValue(ctx, headersKey{}, h)
}

// headerTransport adds the headers from the request context.
type headerTransport struct {
	inner http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h, _ := r.Context().Value(headersKey{}).(http.Header)
	if len(h) == 0 {
		return t.inner.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header[k] = v
	}
	return t.inner.RoundTrip(r)
}
