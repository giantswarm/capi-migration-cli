![CI](https://github.com/giantswarm/capi-migration-cli/actions/workflows/ci.yaml/badge.svg)


# capi-migration-cli

### Requirements for migration specific WC
- there has to be pre-created AWS IAM role of the WC AWSAccount called `giantswarm-{CAPI_MC_NAME}-capa-controller`

### Recomendation for the migration
- Before migration, upscale the master node size to 2-3x actual size to make sure API server can handle the load during migration as there might be 1 node to handle the traffic at certain point of time.

### Requirements to run the tool
- full working `opsctl credentials aws -i MC -c WC` without extra config.

### Recomendation to run the tool
To ensure there are no interference with kubeconfigs that the tool uses, create a new temporary file for kubeconfig.
```
export KUBECONFIG=$(mktemp)
chmod 600 $KUBECONFIG
```

### exmaples how to run the tool

```
## migrate cluster xyz123 from gauss to golem
./capi-migration-cli --mc-capi golem --mc-vintage gauss --cluster-namespace org-giantswarm --cluster-name xyz123

## migrate cluster xyz123 from gauss to golem , set the worker drain batch size to 6 (drain 5 workers at the same time, usefull for big clusters)
./capi-migration-cli --mc-capi golem --mc-vintage gauss --cluster-namespace org-giantswarm --cluster-name xyz123 --worker-node-drain-batch-size 5
```

### Additional notes
*  The tool uses `opsctl credentials aws` to generate AWS access keys, these keys are only valid for 15 minutes. It usually happens that the keys expire duing the run. The tool will detect expired keys and will try generate a new set of keys, this will require additional entry of you MFA token. You should watch the oputput and react to it.

This tools executed folowing steps
### Steps:

* Init Phase
  * get Vintage MC kubeconfig via `opsctl login`
  * get WC cluster kubeconfig via `opsctl login`
  * get CAPI MC kubeconfig via opsctl login
  * generate AWS credential neceseery to work with the WC cluster AWS account - via opsctl credentials aws
  * create a vault client to Vintage Vault via opsctl create vaultconfig

* Prepare Migration Phase
  * fetch all vintage CRs  representing infrastructure of WC cluster in VIntage MC
  * migrate secrets to CAPI mc 
    * migrate CA certs - fetching CA private key from vault via hacked vault - https://github.com/giantswarm/vault-hack/tree/v1.7.10-hack, create CA secret on MC
    * copy encryption provider secret for encrypting ETCD to CAPI MC
    * copy service account v2 secret to CAPI MC
  * create migration scripts as secret in the CAPI MC, these are later injected on machines via file reference in cluster-aws
  * migrate aws credentials for the cluster by create a AWSClusterRoleIdentity in the CAPI MC
  * disable machine health check on the Vintage CRs to avoid aws-operator to terminate nodes during migration
  * scale down app operator for the migrated WC on the vintage MC to avoid issues of overwriting apps
  * clean up charts for `cilium`,`coredns`,`vertical-pod-autoscaler-crd`,`aws-ebs-csi-driver`,`aws-cloud-controller-manager` as in CAPI they are managed by `HelmRelease` CR and not by App or Chart CR. THis operation add pause annotation on the chart and remove finalizers and them delete its , leaving app deployed.

* [optional] Stop Vintage CR reconciliation
  * [optional part - needs to be set via flag] 
  * stop vintage Reconcilion on CRs remove all aws-operator labels on the vintage CRs to avoid reconciliation
  * it is optional becasue for testing purposes we still wanna delete the cluster afterwards and removing the cluster labels will make it hard

* Provision CAPI Cluster Phase
  * generate CAPI cluster templates - generates APP CRs and the configmaps
  * applies the generated templates to the cluster to start the migration
  * start a process in separate go routine that run until end of the cli run - this goroutine will look for CAPI control plane aws machines and add them to the vintage ELBS to keep the old elb active, this is needed as the cp nodes roll over the time so we need to be sureit is always up to date
  * wait until 1 CAPI control-plane node join the cluster and is in Ready state
  * clean migration configuration for etcd - remove hardcoded initial etcd cluster value - from configmap in WC and from cluster values cm and reapply to save the state
  * stop control-plane components on the vintage cluster - stop kube-apiserver, kube-controller-manager, kube-scheduler via job that removes the manifests from the static dir
  * cordon all vintage Control-planes to avoid scheduling new pods there
  * delete app operator pod on the CAPI MC to force new reconcilation to speed up app installation (for new apps like capi-node-labeler)
  * delete chart-operator pod in the WC to reschedule it on the new CAPI control-plane node
  * delete all cilium pods in the WC to ensure fast update fo the helm release (to skip waiting for one by one roll), this is necessery to force running post upgrde helm jobs for policies
  * start a background goroutine that will force-restart any crashed cilium pod, which can ensure CNI is undisturbed
  * wait until all(3) CAPI control plane nodes join the cluster and are in Ready state

* Clean Vintage Cluster Phase
  * drain all vintage control-plane nodes
  * delete vintage ASG groups for control-plane nodes (tccpn) and terminate all instances in that ASG groups
  *  before migrating workloads wait until KubeadmControlPlane CR stabilises which means  all replicas are up to date and ready
  * sequentially for each node pool:
    * drain all vintage worker nodes for the nodepool
    * delete all vintage ASGs for the nodepool

   
## Cleaning clusters
If the migration is only for testing you should delete the cluster in both MCs that measn triiger deletion vintage via happa and delete all genrated app CRs in CAPI MC.
