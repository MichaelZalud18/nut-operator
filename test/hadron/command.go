//go:build hadron

package hadron

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialGuest bounds and performs the SSH transport/handshake shared by every guest command,
// returning a client and a closer that also cancels any blocked operation still in flight when
// the caller's context ends -- the same guarantee guestCommand always gave, now shared with
// guestCommandStdin rather than duplicated.
func dialGuest(ctx context.Context, creds Credentials) (*ssh.Client, func(), error) {
	address := net.JoinHostPort("127.0.0.1", creds.Port)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	config := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.Password(creds.Pass)},
		// Disposable loopback-only guest, matching PEG's trust model. This is not
		// cluster/VM identity verification for the future multi-node harness.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- isolated test guest
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		stop()
		_ = conn.Close()
		return nil, nil, err
	}
	client := ssh.NewClient(clientConn, channels, requests)
	return client, func() { stop(); _ = client.Close() }, nil
}

// guestCommand bounds the entire SSH exchange, including handshake and remote execution.
// Closing the transport cancels blocked operations without abandoning a command goroutine.
func guestCommand(ctx context.Context, creds Credentials, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, closeClient, err := dialGuest(ctx, creds)
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	defer closeClient()
	session, err := client.NewSession()
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	defer func() { _ = session.Close() }()
	out, err := session.CombinedOutput(command)
	return string(out), errors.Join(ctx.Err(), err)
}

// guestCommandStdin runs command on the guest with in streamed to the remote process's stdin,
// for payloads too large to embed in a shell command line -- a container image tarball, here.
// A longer bound than guestCommand's: this exists specifically to move real, non-trivial amounts
// of data, not to run a short command.
func guestCommandStdin(ctx context.Context, creds Credentials, command string, in io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client, closeClient, err := dialGuest(ctx, creds)
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	defer closeClient()
	session, err := client.NewSession()
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	defer func() { _ = session.Close() }()
	session.Stdin = in
	// A single unsynchronized bytes.Buffer given to both Stdout and Stderr would race: the
	// library copies each stream in its own goroutine. syncWriter serializes both into one
	// buffer, the same combined-output guarantee CombinedOutput gives guestCommand.
	out := &syncWriter{}
	session.Stdout = out
	session.Stderr = out
	err = session.Run(command)
	return out.String(), errors.Join(ctx.Err(), err)
}

// syncWriter lets two concurrent copiers (stdout and stderr) safely share one buffer.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
