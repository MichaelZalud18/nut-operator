package main

import (
	"fmt"
	"strings"
)

func validateProfile(profile, image string, webhooks bool) error {
	switch profile {
	case "full":
		if image != "" {
			return fmt.Errorf("--nut-server-image is only supported with --profile=nut-only")
		}
	case "nut-only":
		if strings.TrimSpace(image) == "" || strings.ContainsAny(image, " \t\r\n") {
			return fmt.Errorf("nut-only requires a nonempty --nut-server-image reference")
		}
		if !webhooks {
			return fmt.Errorf("nut-only requires UPSDevice and NUTServer admission; ENABLE_WEBHOOKS=false is unsupported")
		}
	default:
		return fmt.Errorf("unknown profile %q; use full or nut-only", profile)
	}
	return nil
}
