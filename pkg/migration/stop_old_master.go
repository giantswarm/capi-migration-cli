package migration

import (
	"context"
	"fmt"

	"github.com/giantswarm/microerror"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (m *azureMigrator) stopOldMasterComponents(ctx context.Context) error {
	// Get old master node name.
	nodeNames, err := m.getLegacyMasterNodeNames(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// Using the kube-proxy service account because I am sure it exists and it can use the privileged PSP.
	serviceAccountName := "kube-proxy"
	podNamespace := "kube-system"
	m.logger.Debugf(ctx, "found %d legacy nodes", len(nodeNames))

	for _, nodeName := range nodeNames {
		podName := fmt.Sprintf("disable-master-node-components-%s", nodeName)

		// Check if pod already exists.
		existing := corev1.Pod{}
		err = m.wcCtrlClient.Get(ctx, client.ObjectKey{Name: podName, Namespace: podNamespace}, &existing)
		if errors.IsNotFound(err) {
			m.logger.Debugf(ctx, "creating pod for node %s", podName)

			// Create pod definition.
			var pod corev1.Pod
			{
				command := `
([ -f /host/etc/kubernetes/manifests/k8s-controller-manager.yaml ] && mv /host/etc/kubernetes/manifests/k8s-controller-manager.yaml /host/root/) || true ;
([ -f /host/etc/kubernetes/manifests/k8s-api-server.yaml ] && mv /host/etc/kubernetes/manifests/k8s-api-server.yaml /host/root/) || true
`

				pod = corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      podName,
						Namespace: podNamespace,
					},
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
				}
			}

			// Create pod.
			err = m.wcCtrlClient.Create(ctx, &pod)
			if err != nil {
				return microerror.Mask(err)
			}

			m.logger.Debugf(ctx, "created pod for node %s", podName)
			return nil
		} else if err != nil {
			return microerror.Mask(err)
		}

		m.logger.Debugf(ctx, "pod for node %s was already found", podName)
	}

	return nil
}

func (m *azureMigrator) getLegacyMasterNodeNames(ctx context.Context) ([]string, error) {
	nodeList := corev1.NodeList{}
	err := m.wcCtrlClient.List(ctx, &nodeList, client.MatchingLabels{"role": "master"})
	if err != nil {
		return nil, microerror.Mask(err)
	}

	var ret []string

	for _, n := range nodeList.Items {
		ret = append(ret, n.Name)
	}

	return ret, nil
}
