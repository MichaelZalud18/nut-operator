/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package netbox imports NetBox DCIM data into nut-operator's small inventory
// contract. It deliberately speaks the REST API directly instead of depending
// on NetBox model structs: only the planner-owned fields cross this boundary.
package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultOperatorCustomField = "nut_operator"
	defaultPageLimit           = 200
)

// TokenScheme selects the authorization header prefix used for NetBox API
// requests. Modern NetBox v2 tokens use Bearer; legacy v1 tokens use Token.
type TokenScheme string

const (
	TokenSchemeBearer TokenScheme = "Bearer"
	TokenSchemeToken  TokenScheme = "Token"
)

// Client reads NetBox REST API list endpoints.
type Client struct {
	baseURL    *url.URL
	token      string
	scheme     TokenScheme
	httpClient *http.Client
	pageLimit  int
}

// ClientOptions configures a NetBox REST client.
type ClientOptions struct {
	URL         string
	Token       string
	TokenScheme TokenScheme
	HTTPClient  *http.Client
	PageLimit   int
}

// NewClient constructs a NetBox REST client.
func NewClient(options ClientOptions) (*Client, error) {
	if strings.TrimSpace(options.URL) == "" {
		return nil, fmt.Errorf("netbox url is required")
	}
	parsed, err := url.Parse(options.URL)
	if err != nil {
		return nil, fmt.Errorf("parse netbox url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("netbox url must include scheme and host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("netbox url scheme must be http or https")
	}

	scheme := options.TokenScheme
	if scheme == "" {
		scheme = TokenSchemeBearer
	}
	switch scheme {
	case TokenSchemeBearer, TokenSchemeToken:
	default:
		return nil, fmt.Errorf("unsupported netbox token scheme %q", scheme)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	pageLimit := options.PageLimit
	if pageLimit <= 0 {
		pageLimit = defaultPageLimit
	}

	return &Client{
		baseURL:    parsed,
		token:      strings.TrimSpace(options.Token),
		scheme:     scheme,
		httpClient: httpClient,
		pageLimit:  pageLimit,
	}, nil
}

// Fetch reads the NetBox objects needed to render nut-operator inventory.
func (c *Client) Fetch(ctx context.Context, options FetchOptions) (Source, error) {
	deviceQuery := cloneValues(options.DeviceFilters)
	devices, err := list[Device](ctx, c, "/api/dcim/devices/", deviceQuery)
	if err != nil {
		return Source{}, err
	}

	var powerPorts []PowerPort
	var interfaces []Interface
	for _, device := range devices {
		query := url.Values{}
		query.Set("device_id", strconv.Itoa(device.ID))
		nextPorts, err := list[PowerPort](ctx, c, "/api/dcim/power-ports/", query)
		if err != nil {
			return Source{}, err
		}
		powerPorts = append(powerPorts, nextPorts...)

		if options.SkipInterfaces {
			continue
		}
		nextInterfaces, err := list[Interface](ctx, c, "/api/dcim/interfaces/", query)
		if err != nil {
			return Source{}, err
		}
		interfaces = append(interfaces, nextInterfaces...)
	}

	observedAt := options.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	return Source{
		ObservedAt: observedAt,
		Devices:    devices,
		PowerPorts: powerPorts,
		Interfaces: interfaces,
	}, nil
}

// FetchOptions controls which NetBox data is read.
type FetchOptions struct {
	DeviceFilters  url.Values
	SkipInterfaces bool
	ObservedAt     time.Time
}

func list[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	query = cloneValues(query)
	if query.Get("limit") == "" {
		query.Set("limit", strconv.Itoa(c.pageLimit))
	}

	nextURL := c.endpoint(path, query)
	var values []T
	seenPages := map[string]struct{}{}
	for nextURL != "" {
		if _, seen := seenPages[nextURL]; seen {
			return nil, fmt.Errorf("netbox pagination loop at %s", redactedURL(nextURL))
		}
		seenPages[nextURL] = struct{}{}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", fmt.Sprintf("%s %s", c.scheme, c.token))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		page, err := decodePage[T](resp)
		if err != nil {
			return nil, err
		}
		values = append(values, page.Results...)

		nextURL = ""
		if page.Next != "" {
			nextURL, err = c.absoluteURL(page.Next)
			if err != nil {
				return nil, err
			}
		}
	}
	return values, nil
}

func decodePage[T any](resp *http.Response) (paginatedResponse[T], error) {
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return paginatedResponse[T]{}, fmt.Errorf("netbox api returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var page paginatedResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return paginatedResponse[T]{}, fmt.Errorf("decode netbox response: %w", err)
	}
	return page, nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	next := *c.baseURL
	basePath := strings.TrimRight(c.baseURL.Path, "/")
	next.Path = basePath + "/" + strings.TrimLeft(path, "/")
	next.RawQuery = query.Encode()
	return next.String()
}

func (c *Client) absoluteURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	resolved := c.baseURL.ResolveReference(parsed)
	if !sameOrigin(c.baseURL, resolved) {
		return "", fmt.Errorf("netbox pagination next URL %q does not match configured NetBox origin %q", raw, c.baseURL.Redacted())
	}
	return resolved.String(), nil
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func redactedURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.Redacted()
}

func cloneValues(values url.Values) url.Values {
	copied := url.Values{}
	for key, items := range values {
		copied[key] = append([]string(nil), items...)
	}
	return copied
}

type paginatedResponse[T any] struct {
	Next    string `json:"next"`
	Results []T    `json:"results"`
}
