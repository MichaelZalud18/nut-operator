package netbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type redirectTransport func(*http.Request) (*http.Response, error)

func (f redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectOriginBoundary(t *testing.T) {
	for _, target := range []string{"http://netbox.example/api/next", "https://netbox.example:8443/api/next", "https://sub.netbox.example/api/next", "https://other.example/api/next"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			transport := redirectTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`)), Request: r}, nil
				}
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})
			client, err := NewClient(ClientOptions{URL: "https://netbox.example", Token: "fixture-token", HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Fetch(context.Background(), FetchOptions{})
			if err == nil || calls != 1 {
				t.Fatalf("redirect escaped origin: requests=%d err=%v", calls, err)
			}
		})
	}
}

func TestSameOriginRedirectAndLoopLimit(t *testing.T) {
	for _, loop := range []bool{false, true} {
		calls := 0
		original := &http.Client{Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Error("lost same-origin auth")
			}
			if calls == 1 || loop {
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/next"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`)), Request: r}, nil
		})}
		client, err := NewClient(ClientOptions{URL: "https://netbox.example", Token: "fixture-token", HTTPClient: original})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Fetch(context.Background(), FetchOptions{})
		if loop && (err == nil || calls != 10) {
			t.Fatalf("redirect loop not bounded: calls=%d err=%v", calls, err)
		}
		if !loop && (err != nil || calls != 2) {
			t.Fatalf("same-origin redirect rejected: %v", err)
		}
		if original.CheckRedirect != nil {
			t.Fatal("mutated supplied client")
		}
	}
}

func TestRedirectPreservesCallerPolicy(t *testing.T) {
	denied := errors.New("caller rejects redirects")
	calls := 0
	original := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return denied }, Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/next"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	client, err := NewClient(ClientOptions{URL: "https://netbox.example", HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), FetchOptions{})
	if !errors.Is(err, denied) || calls != 1 {
		t.Fatalf("caller policy ignored: calls=%d err=%v", calls, err)
	}
	if original.CheckRedirect == nil {
		t.Fatal("cleared caller policy")
	}
}
