package main

import "testing"

func TestProfile(t *testing.T) {
	for _, tc := range []struct {
		profile, image  string
		webhooks, valid bool
	}{
		{"full", "", true, true}, {"full", "", false, true},
		{"nut-only", "example.com/nut:v1", true, true},
		{"nut-only", "", true, false}, {"nut-only", "bad image", true, false},
		{"nut-only", "example.com/nut:v1", false, false},
		{"typo", "", true, false}, {"full", "example.com/nut:v1", true, false},
	} {
		if err := validateProfile(tc.profile, tc.image, tc.webhooks); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
