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
# get ETCDCTL
DOWNLOAD_URL=https://github.com/etcd-io/etcd/releases/download
ETCD_VER=v3.5.6
rm -f /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
rm -rf /tmp/etcd && mkdir -p /tmp/etcd
curl -L ${DOWNLOAD_URL}/${ETCD_VER}/etcd-${ETCD_VER}-linux-amd64.tar.gz -o /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
tar xzvf /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz -C /tmp/etcd --strip-components=1
rm -f /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
/tmp/etcd/etcdctl version

# get machine IP
IP=$(ip route | grep default | awk '{print $9}')

# add new member to the old etcd cluster
while ! new_cluster=$(/tmp/etcd/etcdctl \
	--cacert=/etc/kubernetes/pki/etcd/ca.crt \
	--key=/etc/kubernetes/pki/etcd/server.key \
	--cert=/etc/kubernetes/pki/etcd/server.crt\
	--endpoints=https://{{.ETCDEndpoint}}:2379 \
	--peer-urls="https://${IP}:2380" \
	member \
	add \
	$(hostname -A) | grep 'ETCD_INITIAL_CLUSTER=')
do
	echo "retrying in 2s"
	sleep 2s
done

echo "successfully added a new member to the old etcd cluster"

# export ETCD_INITIAL_CLUSTER env for later envsubst command
export ${new_cluster}

# copy tmpl
cp /tmp/kubeadm.yaml /tmp/kubeadm.yaml.tmpl

# fill the initial cluster variable into kubeadm config
envsubst < /tmp/kubeadm.yaml.tmpl > /tmp/kubeadm.yaml`

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
