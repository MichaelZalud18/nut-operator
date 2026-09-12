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

// Package nut implements the small NUT protocol surface needed for telemetry
// polling. It does not own Kubernetes resources, shutdown policy, or audit
// persistence.
package nut

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPort         = 3493
	defaultTimeout      = 5 * time.Second
	defaultMaxLineBytes = 64 * 1024
)

// DialContext opens a network connection.
type DialContext func(ctx context.Context, network, address string) (net.Conn, error)

// Target identifies one UPS served by an upsd endpoint.
type Target struct {
	Host    string
	Port    int
	UPSName string
	TLS     TLSOptions
}

type TLSMode string

const (
	TLSDisabled      TLSMode = "Disabled"
	TLSOpportunistic TLSMode = "Opportunistic"
	TLSRequired      TLSMode = "Required"
)

// TLSOptions selects STARTTLS and the trust material for one endpoint.
// An empty mode preserves plaintext clients; an empty CA bundle uses system roots.
type TLSOptions struct {
	Mode       TLSMode
	CABundle   []byte
	ServerName string
}

// ClientOptions configure a NUT protocol client.
type ClientOptions struct {
	DialContext  DialContext
	Timeout      time.Duration
	MaxLineBytes int
}

// Client polls read-only variables from a NUT Attachment Daemon.
type Client struct {
	dialContext  DialContext
	timeout      time.Duration
	maxLineBytes int
}

// NewClient returns a NUT client with production-safe defaults.
func NewClient(options ClientOptions) *Client {
	dialContext := options.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes == 0 {
		maxLineBytes = defaultMaxLineBytes
	}
	return &Client{
		dialContext:  dialContext,
		timeout:      timeout,
		maxLineBytes: maxLineBytes,
	}
}

// ListVariables returns all variables currently exposed for target. The client
// intentionally uses the read-only LIST VAR command and does not authenticate or
// issue administrative NUT commands.
func (c *Client) ListVariables(ctx context.Context, target Target) (map[string]string, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	if target.Port == 0 {
		target.Port = DefaultPort
	}

	pollCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := c.dialContext(pollCtx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
	if err != nil {
		return nil, fmt.Errorf("connect to NUT server %s: %w", target.Address(), err)
	}
	defer func() {
		_ = conn.Close()
	}()
	// A deadline bounds the whole poll, including dialing and TLS. Closing the raw
	// transport also interrupts I/O when the parent is canceled before that deadline.
	stop := context.AfterFunc(pollCtx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := pollCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	transport, err := c.negotiateTLS(pollCtx, conn, target)
	if err != nil {
		return nil, err
	}

	if _, err := fmt.Fprintf(transport, "LIST VAR %s\n", target.UPSName); err != nil {
		return nil, fmt.Errorf("send LIST VAR for UPS %q: %w", target.UPSName, err)
	}
	variables, err := readListVariableResponse(transport, target.UPSName, c.maxLineBytes)
	if err != nil {
		return nil, fmt.Errorf("read LIST VAR response for UPS %q: %w", target.UPSName, err)
	}
	return variables, nil
}

func (c *Client) negotiateTLS(ctx context.Context, conn net.Conn, target Target) (net.Conn, error) {
	options := target.TLS
	switch options.Mode {
	case "", TLSDisabled:
		return conn, nil
	case TLSRequired, TLSOpportunistic:
	default:
		return nil, fmt.Errorf("unsupported NUT TLS mode %q", options.Mode)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: options.ServerName}
	if config.ServerName == "" {
		config.ServerName = target.Host
	}
	if len(options.CABundle) > 0 {
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(options.CABundle) {
			return nil, errors.New("NUT TLS trust bundle contains no certificates")
		}
	}
	if _, err := fmt.Fprint(conn, "STARTTLS\n"); err != nil {
		return nil, fmt.Errorf("send NUT STARTTLS: %w", err)
	}
	reader := bufio.NewReaderSize(conn, c.maxLineBytes)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read NUT STARTTLS response: %w", err)
	}
	reply := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	if reader.Buffered() != 0 {
		return nil, errors.New("unexpected data after NUT STARTTLS response")
	}
	if reply != "OK STARTTLS" {
		if options.Mode == TLSOpportunistic && (reply == "ERR FEATURE-NOT-SUPPORTED" || reply == "ERR FEATURE-NOT-CONFIGURED") {
			return conn, nil
		}
		return nil, fmt.Errorf("NUT STARTTLS refused: %q", reply)
	}
	secure := tls.Client(conn, config)
	if err := secure.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("NUT TLS handshake: %w", err)
	}
	return secure, nil
}

// Address returns host:port with the default NUT port applied.
func (t Target) Address() string {
	port := t.Port
	if port == 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

func validateTarget(target Target) error {
	if strings.TrimSpace(target.Host) == "" {
		return errors.New("NUT target host is required")
	}
	if strings.TrimSpace(target.UPSName) == "" {
		return errors.New("NUT target UPS name is required")
	}
	if strings.ContainsAny(target.UPSName, " \t\r\n\"") {
		return fmt.Errorf("NUT target UPS name %q contains unsupported characters", target.UPSName)
	}
	if target.Port < 0 || target.Port > 65535 {
		return fmt.Errorf("NUT target port %d is outside TCP port range", target.Port)
	}
	return nil
}

func readListVariableResponse(conn net.Conn, upsName string, maxLineBytes int) (map[string]string, error) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), maxLineBytes)

	begin := "BEGIN LIST VAR " + upsName
	end := "END LIST VAR " + upsName
	variables := map[string]string{}
	sawBegin := false

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "ERR ") {
			return nil, errors.New(line)
		}
		if !sawBegin {
			if line != begin {
				return nil, fmt.Errorf("expected %q, got %q", begin, line)
			}
			sawBegin = true
			continue
		}
		if line == end {
			return variables, nil
		}
		name, value, err := parseVariableLine(line, upsName)
		if err != nil {
			return nil, err
		}
		variables[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawBegin {
		return nil, fmt.Errorf("expected %q before end of stream", begin)
	}
	return nil, fmt.Errorf("expected %q before end of stream", end)
}

func parseVariableLine(line, upsName string) (string, string, error) {
	parts := strings.SplitN(line, " ", 4)
	if len(parts) != 4 || parts[0] != "VAR" || parts[1] != upsName || parts[2] == "" {
		return "", "", fmt.Errorf("invalid LIST VAR line %q", line)
	}
	value, err := parseQuotedValue(strings.TrimSpace(parts[3]))
	if err != nil {
		return "", "", fmt.Errorf("invalid value for NUT variable %q: %w", parts[2], err)
	}
	return parts[2], value, nil
}

func parseQuotedValue(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("expected quoted value, got %q", raw)
	}
	var builder strings.Builder
	escaped := false
	for _, r := range raw[1 : len(raw)-1] {
		switch {
		case escaped:
			builder.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		default:
			builder.WriteRune(r)
		}
	}
	if escaped {
		return "", errors.New("quoted value ends with an unfinished escape")
	}
	return builder.String(), nil
}
