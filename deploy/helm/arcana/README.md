# Arcana Helm chart

Installs the Arcana server on one node, driving that node's Docker Engine. **It grants the pod
root-equivalent control of the node.** The trade-offs (hostPath data directory at the same path, one pinned
replica, Docker Engine required on the node, Pod Security, plain HTTP) and the install steps are in
[docs/kubernetes.md](../../../docs/kubernetes.md).

```sh
helm install arcana deploy/helm/arcana --namespace arcana \
  --set nodeName=<node> --set docker.socketGroup=<docker socket GID>
```

Check the chart with `make helm-check` (needs `helm`, `kubeconform` and PyYAML).
