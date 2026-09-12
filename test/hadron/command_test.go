//go:build hadron

package hadron

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGuestCommandCancelsBlockedSSH(t *testing.T) {
	for _, phase := range []string{"handshake", "session", "command"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(key)
			if err != nil {
				t.Fatal(err)
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(signer)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			release := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if phase == "handshake" {
					cancel()
					<-release
					return
				}
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				channel := <-channels
				if channel == nil {
					return
				}
				if phase == "session" {
					cancel()
					<-release
					return
				}
				ch, reqs, err := channel.Accept()
				if err != nil {
					return
				}
				defer func() { _ = ch.Close() }()
				for req := range reqs {
					if req.Type == "exec" {
						_ = req.Reply(true, nil)
						cancel()
						<-release
						return
					}
					_ = req.Reply(false, nil)
				}
			}()
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			_, err = guestCommand(ctx, Credentials{User: "test", Port: port}, "true")
			close(release)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected cancellation in %s, got %v", phase, err)
			}
			<-done
		})
	}
}
