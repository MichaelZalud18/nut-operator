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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/netbox"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var filters repeatedKV
	var tags repeatedString
	var (
		netBoxURL      string
		tokenFile      string
		tokenEnv       string
		tokenScheme    string
		operatorField  string
		timeout        time.Duration
		outputFormat   string
		skipInterfaces bool
	)

	flags := flag.NewFlagSet("netbox-inventory-sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&netBoxURL, "url", "", "NetBox base URL, for example https://netbox.example.com")
	flags.StringVar(&tokenFile, "token-file", "", "file containing a NetBox API token")
	flags.StringVar(&tokenEnv, "token-env", "NETBOX_TOKEN", "environment variable containing a NetBox API token")
	flags.StringVar(&tokenScheme, "token-scheme", "Bearer", "NetBox authorization scheme: Bearer for v2 tokens, Token for legacy v1 tokens")
	flags.StringVar(&operatorField, "operator-field", netbox.DefaultOperatorCustomField, "NetBox custom field containing nut-operator metadata")
	flags.Var(&tags, "tag", "NetBox device tag slug to import; repeat for multiple tags")
	flags.Var(&filters, "filter", "additional NetBox device filter in key=value form; repeat for multiple filters")
	flags.DurationVar(&timeout, "timeout", 30*time.Second, "HTTP request timeout")
	flags.StringVar(&outputFormat, "format", "yaml", "output format: yaml or snapshot-json")
	flags.BoolVar(&skipInterfaces, "skip-interfaces", false, "skip interface reads and do not generate carries edges")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if netBoxURL == "" {
		return errors.New("-url is required")
	}

	token, err := readToken(tokenFile, tokenEnv)
	if err != nil {
		return err
	}
	query := url.Values{}
	for _, tag := range tags {
		query.Add("tag", tag)
	}
	for _, filter := range filters {
		query.Add(filter.key, filter.value)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := netbox.NewClient(netbox.ClientOptions{
		URL:         netBoxURL,
		Token:       token,
		TokenScheme: netbox.TokenScheme(tokenScheme),
		HTTPClient:  &http.Client{Timeout: timeout},
	})
	if err != nil {
		return err
	}
	source, err := client.Fetch(ctx, netbox.FetchOptions{
		DeviceFilters:  query,
		SkipInterfaces: skipInterfaces,
	})
	if err != nil {
		return err
	}
	manifest, err := netbox.BuildManifest(source, netbox.MappingOptions{OperatorCustomField: operatorField})
	if err != nil {
		return err
	}
	// A remaining valid feed can hide an omitted secondary supply from compilation.
	// Refuse a partial power graph even when the mapper can render its other edges.
	for _, diagnostic := range manifest.Diagnostics {
		if diagnostic.Reason == "PowerEndpointUnmapped" {
			return fmt.Errorf("NetBox inventory contains an unmapped power endpoint at %s", diagnostic.Subject)
		}
	}

	if _, diagnostics, err := inventory.Compile(manifest.Snapshot); err != nil {
		for _, diagnostic := range diagnostics {
			_, _ = fmt.Fprintf(stderr, "%s %s %s: %s\n", diagnostic.Severity, diagnostic.Reason, diagnostic.Subject, diagnostic.Message)
		}
		return fmt.Errorf("rendered NetBox inventory is not structurally valid: %w", err)
	}
	for _, diagnostic := range manifest.Diagnostics {
		_, _ = fmt.Fprintf(stderr, "%s %s %s: %s\n", diagnostic.Severity, diagnostic.Reason, diagnostic.Subject, diagnostic.Message)
	}

	var output []byte
	switch outputFormat {
	case "yaml":
		output, err = manifest.YAML()
	case "snapshot-json":
		output, err = manifest.SnapshotJSON()
	default:
		return fmt.Errorf("unsupported format %q", outputFormat)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(output)
	return err
}

func readToken(tokenFile, tokenEnv string) (string, error) {
	if tokenFile != "" {
		content, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		return strings.TrimSpace(string(content)), nil
	}
	if tokenEnv != "" {
		return strings.TrimSpace(os.Getenv(tokenEnv)), nil
	}
	return "", nil
}

type repeatedString []string

func (r *repeatedString) String() string {
	return strings.Join(*r, ",")
}

func (r *repeatedString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*r = append(*r, value)
	}
	return nil
}

type repeatedKV []kv

type kv struct {
	key   string
	value string
}

func (r *repeatedKV) String() string {
	var values []string
	for _, item := range *r {
		values = append(values, item.key+"="+item.value)
	}
	return strings.Join(values, ",")
}

func (r *repeatedKV) Set(value string) error {
	key, item, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("filter must be key=value")
	}
	*r = append(*r, kv{key: strings.TrimSpace(key), value: strings.TrimSpace(item)})
	return nil
}
