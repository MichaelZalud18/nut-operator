package main

import (
	"testing"
	"time"
)

func TestOptionsDefaults(t *testing.T) {
	opts, err := options(func(string) string { return "" })
	if err != nil || opts.ConfigDir != "/etc/nut" || opts.Interval != 5*time.Second {
		t.Fatalf("defaults: %+v, %v", opts, err)
	}
}

func TestOptionsRejectInvalidIntervals(t *testing.T) {
	for _, value := range []string{"0", "00", "-1", "1; exit 0", "1.5", "9223372037", "18446744073709551616"} {
		t.Run(value, func(t *testing.T) {
			_, err := options(func(key string) string {
				if key == "NUT_SUPERVISOR_INTERVAL_SECONDS" {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatal("invalid interval accepted")
			}
		})
	}
}

func TestOptionsCustomPaths(t *testing.T) {
	opts, err := options(func(key string) string {
		return map[string]string{"NUT_CONFPATH": "/tmp/fixture", "NUT_SUPERVISOR_INTERVAL_SECONDS": "2"}[key]
	})
	if err != nil || opts.ConfigDir != "/tmp/fixture" || opts.Interval != 2*time.Second {
		t.Fatalf("custom options: %+v, %v", opts, err)
	}
}
