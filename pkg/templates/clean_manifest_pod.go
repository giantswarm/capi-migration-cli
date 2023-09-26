package templates

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CleanManifestsJob(nodeName string) batchv1.Job {
	// Using the kube-proxy service account because I am sure it exists and it can use the privileged PSP.
	serviceAccountName := "cilium"
	podNamespace := "kube-system"

	var job batchv1.Job
	{
		command := `
([ -f /host/etc/kubernetes/manifests/k8s-controller-manager.yaml ] && mv /host/etc/kubernetes/manifests/k8s-controller-manager.yaml /host/root/) || true ;
([ -f /host/etc/kubernetes/manifests/k8s-api-server.yaml ] && mv /host/etc/kubernetes/manifests/k8s-api-server.yaml /host/root/) || true
([ -f /host/etc/kubernetes/manifests/k8s-scheduler.yaml ] && mv /host/etc/kubernetes/manifests/scheduler.yaml /host/root/) || true
([ -f /host/etc/kubernetes/manifests/k8s-api-healthz.yaml ] && mv /host/etc/kubernetes/manifests/k8s-api-healthz.yaml /host/root/) || true
`
		job = batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("disable-cp-%s", nodeName),
				Namespace: podNamespace,
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Volumes: []corev1.Volume{
							{
								Name: "host",
								VolumeSource: corev1.VolumeSource{
									HostPath: &corev1.HostPathVolumeSource{
										Path: "/",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name:  "disable-master-node-components",
								Image: "alpine:latest",
								Command: []string{
									"ash",
									"-c",
									command,
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "host",
										ReadOnly:  false,
										MountPath: "/host",
									},
								},
							},
						},
						NodeName:           nodeName,
						RestartPolicy:      corev1.RestartPolicyNever,
						ServiceAccountName: serviceAccountName,
					},
				},
			},
		}
	}
	return job
}
