package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func validateReadback(before, after, controlBefore, controlAfter observed) error {
	hash, err := hex.DecodeString(before.PayloadSHA256)
	if err != nil || len(hash) != 32 || before.PodUID == "" || !before.Ready {
		return fmt.Errorf("missing original dataset identity")
	}
	if after.PodUID != before.PodUID || after.PayloadSHA256 != before.PayloadSHA256 ||
		!after.Ready || after.SandboxID == "" || after.ContainerID == "" || after.RunID == "" {
		return fmt.Errorf("existing target identity or data was lost")
	}
	if ip, err := netip.ParseAddr(after.IPv6); err != nil || !ip.Is6() {
		return fmt.Errorf("target is not reachable over IPv6")
	}
	if controlBefore.PodUID == "" || controlBefore.PayloadSHA256 == "" || !controlBefore.Ready {
		return fmt.Errorf("missing control baseline")
	}
	return unchanged(controlBefore, controlAfter)
}

// Readback has no allocation, seeding, apply, or repair path. The same final
// rotation evidence is used on both sides of the existing SDK crash sequence.
func (s *scenario) readback(ctx context.Context) error {
	if !regexp.MustCompile(`^prefix-[0-9]+$`).MatchString(s.namespace) {
		return fmt.Errorf("explicit existing prefix qualification namespace required")
	}
	var final struct {
		After   observed `json:"after"`
		Control observed `json:"control"`
	}
	data, err := os.ReadFile(filepath.Join(s.evidence, "round-3.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &final); err != nil {
		return err
	}
	if err := s.connect(ctx); err != nil {
		return err
	}
	var target, control observed
	err = wait(ctx, 5*time.Minute, func(ctx context.Context) error {
		var err error
		target, err = s.observe(ctx, "target")
		if err != nil {
			return err
		}
		control, err = s.observe(ctx, "control")
		return err
	})
	if err != nil {
		return err
	}
	// Connectivity may converge, but a successfully read mismatched dataset
	// is terminal. Never retry a data failure until it happens to disappear.
	return validateReadback(final.After, target, final.Control, control)
}
