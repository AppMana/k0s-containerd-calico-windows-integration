// Package prefixscenario builds the native Kubernetes resources used by the
// live prefix-rotation consumer. It does not install or repair any component.
package prefixscenario

import (
	"encoding/json"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// WindowsWorkload mounts already-created data. DirectoryOrCreate would hide a
// lost host directory after replacement, so only Directory is permitted here.
func WindowsWorkload(namespace, name, image, root, manualPool string) *core.Pod {
	directory, grace := core.HostPathDirectory, int64(30)
	p := &core.Pod{
		TypeMeta: meta.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": "prefix-qualification"},
			Annotations: map[string]string{"cni.projectcalico.org/ipFamilies": `["IPv4","IPv6"]`}},
		Spec: core.PodSpec{
			NodeName: "windows", RestartPolicy: core.RestartPolicyAlways, TerminationGracePeriodSeconds: &grace,
			Containers: []core.Container{{Name: "probe", Image: image, ImagePullPolicy: core.PullNever,
				Command: []string{`C:\tools\prefix-workload.exe`}, Args: []string{"--state-dir", `C:\state`, "--listen", ":8080", "--drain", "5s"},
				ReadinessProbe: &core.Probe{ProbeHandler: core.ProbeHandler{HTTPGet: &core.HTTPGetAction{Path: "/", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 1, TimeoutSeconds: 1},
				VolumeMounts:   []core.VolumeMount{{Name: "state", MountPath: `C:\state`}, {Name: "tools", MountPath: `C:\tools`, ReadOnly: true}},
			}},
			Volumes: []core.Volume{
				{Name: "state", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: root + `\` + name, Type: &directory}}},
				{Name: "tools", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: root + `\bin`, Type: &directory}}},
			},
		},
	}
	if manualPool != "" {
		pools, _ := json.Marshal([]string{manualPool})
		p.Annotations["cni.projectcalico.org/ipv6pools"] = string(pools)
	}
	return p
}

// Stable controls use a manual-only pool so replacement targets cannot escape
// into that still-enabled pool instead of acquiring the next tested prefix.
func IPv6Pool(name, prefix, nodeSelector string, disabled, manual bool) *unstructured.Unstructured {
	mode := "Automatic"
	if manual {
		mode = "Manual"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "crd.projectcalico.org/v1", "kind": "IPPool",
		"metadata": map[string]any{"name": name, "labels": map[string]any{"appmana.com/qualification": "prefix-rotation"}},
		"spec": map[string]any{"cidr": prefix, "blockSize": int64(122), "ipipMode": "Never", "vxlanMode": "Never", "natOutgoing": false,
			"nodeSelector": nodeSelector, "disabled": disabled, "assignmentMode": mode},
	}}
}
