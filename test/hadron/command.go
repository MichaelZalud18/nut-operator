//go:build hadron

package hadron

import (
	"context"
	"errors"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// guestCommand bounds the entire SSH exchange, including handshake and remote execution.
// Closing the transport cancels blocked operations without abandoning a command goroutine.
func guestCommand(ctx context.Context, creds Credentials, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	address := net.JoinHostPort("127.0.0.1", creds.Port)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	config := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.Password(creds.Pass)},
		// Disposable loopback-only guest, matching PEG's trust model. This is not
		// cluster/VM identity verification for the future multi-node harness.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- isolated test guest
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return "", errors.Join(ctx.Err(), err)
	}
	defer func() { _ = session.Close() }()
	out, err := session.CombinedOutput(command)
	return string(out), errors.Join(ctx.Err(), err)
}
