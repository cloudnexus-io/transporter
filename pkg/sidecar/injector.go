package sidecar

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	SidecarImage   = "192.168.1.20:5000/transporter-proxy:latest"
	SidecarName    = "transporter-proxy"
	SidecarPort    = 50052
	ManagementPort = 50053
	AppPort        = 80
)

func InjectSidecar(podSpec *corev1.PodSpec, targetIP string, appPort int) {
	sidecar := corev1.Container{
		Name:  SidecarName,
		Image: SidecarImage,
		Ports: []corev1.ContainerPort{
			{Name: "proxy", ContainerPort: int32(SidecarPort)},
			{Name: "mgmt", ContainerPort: int32(ManagementPort)},
		},
		Env: []corev1.EnvVar{
			{Name: "TARGET_IP", Value: targetIP},
			{Name: "APP_PORT", Value: fmt.Sprintf("%d", appPort)},
			{Name: "MANAGEMENT_PORT", Value: fmt.Sprintf("%d", ManagementPort)},
			{Name: "MODE", Value: "buffer"},
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_PTRACE"},
			},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/ready",
					Port: intstr.FromInt(ManagementPort),
				},
			},
		},
	}

	podSpec.Containers = append(podSpec.Containers, sidecar)
	podSpec.ShareProcessNamespace = boolPtr(true)
	podSpec.HostNetwork = false
	podSpec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
}

func InjectEBPFSidecar(podSpec *corev1.PodSpec, sourcePodIP, targetNodeIP string, appPort int) {
	sidecar := corev1.Container{
		Name:  "transporter-tap",
		Image: SidecarImage,
		Ports: []corev1.ContainerPort{
			{Name: "tap", ContainerPort: int32(ManagementPort)},
		},
		Env: []corev1.EnvVar{
			{Name: "MODE", Value: "tap"},
			{Name: "SOURCE_POD_IP", Value: sourcePodIP},
			{Name: "TARGET_NODE_IP", Value: targetNodeIP},
			{Name: "TARGET_PORT", Value: fmt.Sprintf("%d", ManagementPort)},
			{Name: "APP_PORT", Value: fmt.Sprintf("%d", appPort)},
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "BPF"},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "bpffs", MountPath: "/sys/fs/bpf"},
			{Name: "cgroup", MountPath: "/sys/fs/cgroup"},
		},
	}

	podSpec.Containers = append(podSpec.Containers, sidecar)
	podSpec.ShareProcessNamespace = boolPtr(true)
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: "bpffs",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/bpf"},
		},
	}, corev1.Volume{
		Name: "cgroup",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/cgroup"},
		},
	})
}

func boolPtr(b bool) *bool {
	return &b
}
