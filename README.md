![CI](https://github.com/giantswarm/capi-migration-cli/actions/workflows/ci.yaml/badge.svg)


# capi-migration-cli

### Requirements for migration specific WC
- there has to be pre-created AWS IAM role of the WC AWSAccount called `giantswarm-{CAPI_MC_NAME}-capa-controller`

### Requirements to run the tool
- full working `opsctl credentials aws -i MC -c WC` without extra config.

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
  * wait until all(3) CAPI control plane nodes join the cluster and are in Ready state

* Clean Vintage Cluster Phase
  * drain all vintage control-plane nodes
  * delete vintage ASG groups for control-plane nodes (tccpn) and terminate all instances in that ASG groups
  * sequentially for each node pool:
    * drain all vintage worker nodes for the nodepool
    * delete all vintage ASGs for the nodepool

   

