package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/util/version"
)

type upgradeBaseline struct {
	RuntimeVersion string   `json:"runtimeVersion"`
	Target         observed `json:"target"`
	Control        observed `json:"control"`
}

func validateUpgradeReadback(before upgradeBaseline, candidate string, target, control observed) error {
	old, err := version.ParseSemantic(before.RuntimeVersion)
	if err != nil {
		return fmt.Errorf("invalid original runtime identity: %w", err)
	}
	next, err := version.ParseSemantic(candidate)
	if err != nil {
		return err
	}
	if !old.LessThan(next) {
		return fmt.Errorf("qualification requires a forward runtime upgrade, not restart or downgrade")
	}
	if err := validateReadback(before.Target, target, before.Control, control); err != nil {
		return err
	}
	// Unlike crash recovery, an orderly daemon-only upgrade must preserve the
	// running shim/task, process RunID, sandbox and endpoint, not merely data.
	return unchanged(before.Target, target)
}

func (s *scenario) upgradeReadback(ctx context.Context) error {
	if !regexp.MustCompile(`^prefix-[0-9]+$`).MatchString(s.namespace) {
		return fmt.Errorf("explicit existing upgrade namespace required")
	}
	data, err := os.ReadFile(filepath.Join(s.evidence, "baseline.json"))
	if err != nil {
		return err
	}
	var before upgradeBaseline
	if err := json.Unmarshal(data, &before); err != nil {
		return err
	}
	// Validate the original evidence before connecting to the lab.
	if err := validateUpgradeReadback(before, s.version, before.Target, before.Control); err != nil {
		return err
	}
	if err := s.connect(ctx); err != nil {
		return err
	}
	var target, control observed
	if err := wait(ctx, 5*time.Minute, func(ctx context.Context) error {
		var err error
		target, err = s.observe(ctx, "target") // Includes native CRI runtime version.
		if err != nil {
			return err
		}
		control, err = s.observe(ctx, "control")
		return err
	}); err != nil {
		return err
	}
	// Identity/data failures are terminal, not retried into a later pass.
	return validateUpgradeReadback(before, s.version, target, control)
}
