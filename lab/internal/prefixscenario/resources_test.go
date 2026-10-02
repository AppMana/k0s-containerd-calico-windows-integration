package prefixscenario

import (
	"reflect"
	"strings"
	"testing"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeObserverSurvivesExecStreamAndCannotRestartSilently(t *testing.T) {
	p := RuntimeObserver("test", "pinned-image", `C:\LabPrefix\test`, "target-uid")
	if p.Spec.NodeName != "windows" || !p.Spec.HostNetwork || !*p.Spec.SecurityContext.WindowsOptions.HostProcess {
		t.Fatal("observer must sample host CRI outside the ordinary pod-network fault domain")
	}
	if p.Spec.RestartPolicy != core.RestartPolicyNever || *p.Spec.AutomountServiceAccountToken {
		t.Fatal("observer must not restart silently or need API credentials")
	}
	c := p.Spec.Containers[0]
	if c.ImagePullPolicy != core.PullNever || len(c.Command) != 1 || c.Command[0] != `C:\LabPrefix\test\bin\prefix-runtime.exe` {
		t.Fatal("kubelet must own the bounded native observer, not an exec/shell background job")
	}
	if !reflect.DeepEqual(c.Args, []string{"--pod-uid", "target-uid", "--watch", `C:\LabPrefix\test\runtime.jsonl`, "--duration", "20m"}) {
		t.Fatal("observer identity, durable evidence path, or lifetime changed")
	}
}

func TestReplacementWorkloadCannotReseedLostDataOrPinOldPool(t *testing.T) {
	p := WindowsWorkload("qualification", "target", "image@sha256:"+strings.Repeat("a", 64), `C:\LabPrefix`, "")
	if p.Spec.HostNetwork || (p.Spec.SecurityContext != nil && p.Spec.SecurityContext.WindowsOptions != nil && p.Spec.SecurityContext.WindowsOptions.HostProcess != nil && *p.Spec.SecurityContext.WindowsOptions.HostProcess) {
		t.Fatal("target must be an ordinary networked container")
	}
	if p.Annotations["cni.projectcalico.org/ipv6pools"] != "" {
		t.Fatal("target must follow enabled pools, not pin the invalidated pool")
	}
	if p.Annotations["cni.projectcalico.org/ipFamilies"] != `["IPv4","IPv6"]` {
		t.Fatal("prefix workload must explicitly request dual-stack IPAM")
	}
	if len(p.Spec.InitContainers) != 0 {
		t.Fatal("replacement must not initialize the original payload")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath == nil || v.HostPath.Type == nil || *v.HostPath.Type != core.HostPathDirectory {
			t.Fatal("lost data directory would be recreated")
		}
	}
	if *p.Spec.TerminationGracePeriodSeconds <= 5 {
		t.Fatal("grace period must allow the observable flush")
	}
	if !p.Spec.Containers[0].VolumeMounts[1].ReadOnly {
		t.Fatal("probe executable must be immutable")
	}
}

func TestStablePoolCannotAuthorizeAutomaticReplacement(t *testing.T) {
	pool := IPv6Pool("stable", "2001:db8:100:80::/64", "kubernetes.io/hostname == 'windows'", false, true)
	mode, _, _ := unstructured.NestedString(pool.Object, "spec", "assignmentMode")
	if mode != "Manual" {
		t.Fatal("replacement could allocate a stable-control address")
	}
	control := WindowsWorkload("qualification", "control", "image", `C:\LabPrefix`, "stable")
	if control.Annotations["cni.projectcalico.org/ipv6pools"] != `["stable"]` {
		t.Fatal("control did not explicitly select the manual pool")
	}
}

func TestStableControlIsOutsideWindowsSharedNetwork(t *testing.T) {
	p := LinuxControl("qualification", "image", "/var/tmp/prefix-test")
	if p.Spec.NodeName != "linux" || p.Spec.HostNetwork {
		t.Fatal("control must be an ordinary Linux pod outside the Windows shared-network fault domain")
	}
	if p.Annotations["cni.projectcalico.org/ipv6pools"] != "" || p.Spec.Containers[0].Command[0] != "/tools/prefix-workload" {
		t.Fatal("control must use Linux IPAM and the real portable workload")
	}
	for _, v := range p.Spec.Volumes {
		if v.HostPath == nil || *v.HostPath.Type != core.HostPathDirectory || !strings.HasPrefix(v.HostPath.Path, "/var/tmp/prefix-test/") {
			t.Fatal("control must mount existing Linux payload and tools without reseeding")
		}
	}
}
