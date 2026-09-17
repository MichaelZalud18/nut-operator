package main

import (
	"bytes"
	"context"
	"testing"
)

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"usage", []string{"unexpected"}, 2},
		{"version", []string{"--version"}, 0},
		{"invalid argument", []string{"--invalid"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit = %d, want %d", code, tc.code)
			}
			if tc.code != 0 && stderr.Len() == 0 {
				t.Fatal("failure needs a diagnostic")
			}
			if tc.name == "version" && stdout.String() != version+"\n" {
				t.Fatalf("version = %q", stdout.String())
			}
		})
	}
}
