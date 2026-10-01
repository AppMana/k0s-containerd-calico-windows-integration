// Package prefixcheck defines the evidence required by the live SDK scenario.
// It observes lifecycle changes; it never repairs routes or restarts workloads.
package prefixcheck

import (
	"encoding/hex"
	"fmt"
	"net/netip"
)

type Observation struct {
	PodUID, SandboxID, ContainerID string
	IPv6                           string
	Ready                          bool
	PayloadSHA256                  string
}

type Shutdown struct {
	ContainerID       string
	Notified, Flushed bool
	Exited            bool
	ExitCode          int
	Forced            bool
}

func ValidateReplacement(before, after Observation, shutdown Shutdown, enabledPrefix netip.Prefix) error {
	if before.PodUID == "" || before.SandboxID == "" || before.ContainerID == "" || !before.Ready {
		return fmt.Errorf("missing ready baseline identity")
	}
	if after.PodUID != before.PodUID || after.SandboxID == "" || after.SandboxID == before.SandboxID || after.ContainerID == "" || after.ContainerID == before.ContainerID || !after.Ready {
		return fmt.Errorf("replacement must be a new ready sandbox/container under the original Pod UID")
	}
	ip, err := netip.ParseAddr(after.IPv6)
	if err != nil || !ip.Is6() || !enabledPrefix.IsValid() || !enabledPrefix.Addr().Is6() || !enabledPrefix.Contains(ip) {
		return fmt.Errorf("replacement IPv6 address is outside enabled prefix")
	}
	oldIP, err := netip.ParseAddr(before.IPv6)
	if err != nil || !oldIP.Is6() || enabledPrefix.Contains(oldIP) {
		return fmt.Errorf("baseline IPv6 address was not invalidated by the new prefix")
	}
	digest, err := hex.DecodeString(before.PayloadSHA256)
	if err != nil || len(digest) != 32 || after.PayloadSHA256 != before.PayloadSHA256 {
		return fmt.Errorf("original acknowledged payload was not retained")
	}
	if shutdown.ContainerID != before.ContainerID || !shutdown.Notified || !shutdown.Flushed || !shutdown.Exited || shutdown.Forced || shutdown.ExitCode != 0 {
		return fmt.Errorf("original container did not demonstrate a graceful flushed exit")
	}
	return nil
}
