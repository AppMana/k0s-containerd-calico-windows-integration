package prefixcheck

import (
	"net/netip"
	"strings"
	"testing"
)

func TestReplacementRejectsFalseSuccess(t *testing.T) {
	before := Observation{"pod", "sandbox-old", "container-old", "2001:db8:1::2", true, strings.Repeat("a", 64)}
	after := Observation{"pod", "sandbox-new", "container-new", "2001:db8:2::2", true, before.PayloadSHA256}
	shutdown := Shutdown{ContainerID: before.ContainerID, Notified: true, Flushed: true, Exited: true}
	prefix := netip.MustParsePrefix("2001:db8:2::/64")
	if err := ValidateReplacement(before, after, shutdown, prefix); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Observation, *Shutdown){
		"missing actual exit":      func(_ *Observation, s *Shutdown) { s.Exited = false },
		"pod recreated":            func(o *Observation, _ *Shutdown) { o.PodUID = "different" },
		"sandbox not replaced":     func(o *Observation, _ *Shutdown) { o.SandboxID = before.SandboxID },
		"same container":           func(o *Observation, _ *Shutdown) { o.ContainerID = before.ContainerID },
		"stale prefix":             func(o *Observation, _ *Shutdown) { o.IPv6 = before.IPv6 },
		"IPv4 only":                func(o *Observation, _ *Shutdown) { o.IPv6 = "10.0.0.2" },
		"not ready":                func(o *Observation, _ *Shutdown) { o.Ready = false },
		"data changed":             func(o *Observation, _ *Shutdown) { o.PayloadSHA256 = strings.Repeat("b", 64) },
		"no shutdown notification": func(_ *Observation, s *Shutdown) { s.Notified = false },
		"no flush":                 func(_ *Observation, s *Shutdown) { s.Flushed = false },
		"forced exit":              func(_ *Observation, s *Shutdown) { s.Forced = true },
		"nonzero exit":             func(_ *Observation, s *Shutdown) { s.ExitCode = 137 },
		"wrong container evidence": func(_ *Observation, s *Shutdown) { s.ContainerID = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			o, s := after, shutdown
			mutate(&o, &s)
			if err := ValidateReplacement(before, o, s, prefix); err == nil {
				t.Fatal("accepted false replacement evidence")
			}
		})
	}
}
