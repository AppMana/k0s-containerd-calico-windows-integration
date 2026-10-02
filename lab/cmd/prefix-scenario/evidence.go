package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/AppMana/k0s-containerd-calico-windows-integration/internal/prefixcheck"
)

func readySandbox(s runtimeSnapshot, uid, version, containerID, ipv6 string) (string, error) {
	if s.PodUID != uid || s.RuntimeVersion != version {
		return "", fmt.Errorf("CRI Pod/runtime identity mismatch")
	}
	for _, c := range s.Containers {
		if c.ID == containerID && c.State == "CONTAINER_RUNNING" {
			for _, b := range s.Sandboxes {
				if b.ID == c.SandboxID && b.State == "SANDBOX_READY" {
					for _, ip := range b.IPs {
						a, e := netip.ParseAddr(ip)
						want, werr := netip.ParseAddr(ipv6)
						if e == nil && werr == nil && a == want {
							return b.ID, nil
						}
					}
				}
			}
		}
	}
	return "", fmt.Errorf("no ready CRI sandbox matches Pod container and IPv6 address")
}

func runtimeShutdown(body []byte, before observed, version string) (prefixcheck.Shutdown, error) {
	shutdown := prefixcheck.Shutdown{ContainerID: before.ContainerID}
	invalidated := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		var record struct {
			Snapshot runtimeSnapshot `json:"snapshot"`
			Error    string          `json:"error"`
		}
		if json.Unmarshal(line, &record) != nil || record.Error != "" {
			continue
		}
		if record.Snapshot.PodUID != before.PodUID || record.Snapshot.RuntimeVersion != version {
			continue
		}
		for _, box := range record.Snapshot.Sandboxes {
			if box.ID == before.SandboxID && box.State == "SANDBOX_NOTREADY" {
				invalidated = true
			}
		}
		for _, c := range record.Snapshot.Containers {
			if c.ID == before.ContainerID && c.SandboxID == before.SandboxID && c.State == "CONTAINER_EXITED" && c.FinishedAt > 0 {
				shutdown.Exited = true
				shutdown.ExitCode = c.ExitCode
			}
		}
	}
	if !invalidated || !shutdown.Exited {
		return shutdown, fmt.Errorf("missing native NOTREADY and old-container exit observations")
	}
	return shutdown, nil
}
