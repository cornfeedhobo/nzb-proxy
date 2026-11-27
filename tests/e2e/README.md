# End-to-end test

Install Go (see `go.mod`), Make, Docker with Buildx, kind, kubectl, and Helm. The test exports Docker image archives for kind. If your builder does not support that export, create one with `docker buildx create --driver docker-container --use`. From the repository root, run:

```sh
make e2e
```

GitHub Actions runs the same command in the `e2e` job. On Linux with the `gh act` extension installed, run the job locally with:

```sh
gh act push -j e2e -P ubuntu-24.04=-self-hosted
```

Each run creates a single-node kind cluster with its own kubeconfig, test keys, and TLS certificate. The first run needs network access for the images. Allow several minutes for both applications to start. The test deletes its cluster during cleanup. If you interrupt the process, use the cluster name in the test output with `kind delete cluster --name`.

## Coverage

The test builds the proxy from the checkout and runs Prowlarr `2.4.0.5391` and Radarr `6.2.0.10390` in the cluster. A simulated Newznab indexer serves a synthetic NZB over HTTPS and counts fetches. Prowlarr syncs the proxy URL into Radarr, and Radarr tests the indexer through the proxy.

The same applications exercise the [Kustomize deployment](../../deploy/kustomize) and then the [Helm chart](../../deploy/helm). Each installation starts with a fresh cache.

- Two searches return different encrypted download links for the same release. Both RSS download fields use the proxy route and the signing key from the Kubernetes Secret.
- Sixteen concurrent requests return the expected NZB bytes. The indexer records one fetch and Prowlarr records one grab.
- After a Kustomize rollout restart or a Helm upgrade, a replacement pod serves the original signed link through the Service. A fresh search also reuses the cache.
- The proxy rejects the wrong API key and a modified signed request. Two requests for invalid NZB content return HTTP 502 and cause one indexer fetch.

Finally, the test stops Prowlarr and the indexer. The original link still returns the cached bytes. The Go client issues the download requests. Sonarr, Radarr movie grabs, and Usenet download clients are outside this suite.
