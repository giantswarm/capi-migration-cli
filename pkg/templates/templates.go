package templates

import (
	"bytes"
	"text/template"

	"github.com/giantswarm/microerror"
)

func RenderTemplate(tmpl string, params interface{}) (string, error) {
	var buff bytes.Buffer
	t := template.Must(template.New("tmpl").Parse(tmpl))

	err := t.Execute(&buff, params)
	if err != nil {
		return "", microerror.Mask(err)
	}
	return buff.String(), nil
}

const AWSJoinCluster = `#!/bin/sh
# only execute on first  node which has kubeadm init command
if [ "$(grep "kubeadm join" /etc/kubeadm.sh | wc -l)" -eq 1 ];then 
	echo "not a first node, waiting until old nodes are cleaned up"
	exit 0
else 
	echo "identified first node, executing join command to vintage etcd cluster"
fi

# get ETCDCTL
DOWNLOAD_URL=https://github.com/etcd-io/etcd/releases/download
ETCD_VER=v3.5.6
rm -f /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
rm -rf /tmp/etcd && mkdir -p /tmp/etcd
curl -L ${DOWNLOAD_URL}/${ETCD_VER}/etcd-${ETCD_VER}-linux-amd64.tar.gz -o /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
tar xzvf /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz -C /tmp/etcd --strip-components=1
rm -f /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
/tmp/etcd/etcdctl version

# prepare etcd client certs
openssl req -new -newkey rsa:4096 -days 365 -nodes -x509 \
    -subj "/C=DE/ST=GiantSwarm/L=Cologne/O=Dis/CN={{.ETCDEndpoint}}" \
    -keyout /etc/kubernetes/pki/etcd/client-key.pem  -out /etc/kubernetes/pki/etcd/client-cert.pem \
    -CA /etc/kubernetes/pki/etcd/ca.crt -CAkey /etc/kubernetes/pki/etcd/ca.key 

ETCDCTL="/tmp/etcd/etcdctl --cacert=/etc/kubernetes/pki/etcd/ca.crt --key=/etc/kubernetes/pki/etcd/client-key.pem --cert=/etc/kubernetes/pki/etcd/client-cert.pem --endpoints=https://{{.ETCDEndpoint}}:2379"
# check if new member is already added to the old etcd cluster
members_count=$( $ETCDCTL member list |  wc -l)

## only add new member if we have 1 or 3 members in etcd cluster
if [ $members_count -eq 1 ] || [ $members_count -eq 3 ]; then
	# get machine IP
	IP=$(ip route | grep default | awk '{print $9}')

	# add new member to the old etcd cluster
	while ! new_cluster=$($ETCDCTL \
		--peer-urls="https://${IP}:2380" \
		member \
		add \
		$(hostname -f) | grep 'ETCD_INITIAL_CLUSTER=')
	do
		echo "retrying in 2s"
		sleep 2s
	done
	
	echo "successfully added a new member to the old etcd cluster"
	
	# export ETCD_INITIAL_CLUSTER env for later envsubst command
	export ${new_cluster}
	
	# replace in kubeadm config file
	cp /etc/kubeadm.yml /etc/kubeadm.yml.tmpl
	envsubst < /etc/kubeadm.yml.tmpl > /etc/kubeadm.yml

	rm -f /etc/kubeadm.yml.tmpl
fi
`

const AWSMoveLeaderCommand = `#!/bin/sh
# only execute on first node which has kubeadm init command
if [ "$(grep "kubeadm join" /etc/kubeadm.sh | wc -l)" -eq 1 ];then 
	echo "not a first node, skipping command"
	exit 0
else 
	echo "identified first node, executing command"
fi

# get etcd node id
etcd_node_id=$(/tmp/etcd/etcdctl \
	--cacert=/etc/kubernetes/pki/etcd/ca.crt \
	--key=/etc/kubernetes/pki/etcd/client-key.pem \
	--cert=/etc/kubernetes/pki/etcd/client-cert.pem\
	--endpoints=https://127.0.0.1:2379 \
	member list | grep $(hostname -f) | cut -d, -f1)

# generate address for all etcd members
ETCD="{{.ETCDEndpoint}}"
# Generate three new variables with incremented numbers
ETCD1="${ETCD/etcd/etcd1}"
ETCD2="${ETCD/etcd/etcd2}"
ETCD3="${ETCD/etcd/etcd3}"
# get leader address

ETCD_LEADER=$(/tmp/etcd/etcdctl \
	--cacert=/etc/kubernetes/pki/etcd/ca.crt \
	--key=/etc/kubernetes/pki/etcd/client-key.pem \
	--cert=/etc/kubernetes/pki/etcd/client-cert.pem\
	--endpoints=https://$ETCD1:2379,https://$ETCD2:2379,https://$ETCD3:2379 \
	endpoint status | grep "true" | cut -d, -f1)

# promote etcd node to leader
/tmp/etcd/etcdctl \
	--cacert=/etc/kubernetes/pki/etcd/ca.crt \
	--key=/etc/kubernetes/pki/etcd/client-key.pem \
	--cert=/etc/kubernetes/pki/etcd/client-cert.pem\
	--endpoints=$ETCD_LEADER \
	move-leader ${etcd_node_id}

`

const APIHealthzVintagePod = `
apiVersion: v1
kind: Pod
metadata:
  name: k8s-api-healthz
  namespace: kube-system
  annotations:
    scheduler.alpha.kubernetes.io/critical-pod: ''
spec:
  hostNetwork: true
  priorityClassName: system-node-critical
  containers:
    - name: k8s-api-healthz
      command:
        - /k8s-api-healthz
        - --etcd-cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt
        - --etcd-key=/etc/kubernetes/pki/etcd/healthcheck-client.key
        - --etcd-ca-cert=/etc/kubernetes/pki/etcd/ca.crt
        - --api-cert=/etc/kubernetes/pki/apiserver.crt
        - --api-key=/etc/kubernetes/pki/apiserver.key
        - --api-ca-cert=/etc/kubernetes/pki/ca.crt
      image: quay.io/giantswarm/k8s-api-healthz:0.2.0
      resources:
        requests:
          cpu: 50m
          memory: 20Mi
      volumeMounts:
      - mountPath: /etc/kubernetes/pki/
        name: ssl-certs-kubernetes
        readOnly: true
  volumes:
  - hostPath:
      path: /etc/kubernetes/pki
    name: ssl-certs-kubernetes`

const AddExtraServiceAccountIssuersScript = `#!/bin/sh
{{ $issuer:= range .ExtraServiceAccountIssuers }}
sed -i '/- --tls-private-key-file=\/etc\/kubernetes\/pki\/apiserver.key$/s/$/\n    - --service-account-issuer={{ $issuer }}/'
{{ $issuer }}
{{ end }}
`

const AppCRTemplate = `
{{- if .UserConfigConfigMap -}}
---
{{ .UserConfigConfigMap -}}
{{- end -}}
{{- if .UserConfigSecret -}}
---
{{ .UserConfigSecret -}}
{{- end -}}
---
{{ .AppCR -}}
---
`
