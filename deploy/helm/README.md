# Install NZB proxy with Helm

Create a Secret named `nzb-proxy` in the release namespace. Store the Prowlarr API key under `api-key` and 32 raw random bytes under `signing-key`. Keep the signing key across upgrades so existing download links stay valid. If the Secret has another name, set `existingSecret` to that name.

Save your overrides in a values file outside the repository. Set `config.publicURL` to an address that Prowlarr, Radarr, and Sonarr can reach. By default, the chart uses the short Service name in the release namespace. List each external download or redirect host in `config.allowedHosts`.

```yaml
config:
  upstreamURL: http://prowlarr:9696
  publicURL: http://nzb-proxy:8080
  allowedHosts:
    - indexer.example
```

To install a published release, select its chart version and the namespace that contains the Secret. For example, after the `v1.0.0` release:

```sh
helm upgrade --install nzb-proxy oci://ghcr.io/cornfeedhobo/charts/nzb-proxy \
  --version 1.0.0 --namespace media -f /path/to/values.yaml --wait
```

To install from a checkout, run this command in the repository root.

```sh
helm upgrade --install nzb-proxy deploy/helm --namespace media -f /path/to/values.yaml --wait
```

The chart runs one pod. During upgrades that replace the pod, the proxy briefly stops while the old pod releases the cache. By default, Helm creates a 1 GiB claim with the default StorageClass. Uninstalling the release deletes that claim. To keep the cache across uninstalls, create the claim separately and set `persistence.existingClaim` before installation.

Released charts use the matching image tag. The source chart defaults to `latest`. To pin the image by digest, set `image.repository` to the full reference, including `@sha256:…`. See [values.yaml](values.yaml) for all chart values.
