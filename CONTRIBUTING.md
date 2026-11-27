# Contributing

## Development

### Publishing images and Helm charts

After all test jobs pass, pushes and merges to `main` publish `ghcr.io/cornfeedhobo/nzb-proxy:latest` and `ghcr.io/cornfeedhobo/nzb-proxy:sha-<full-commit-sha>`. These binaries report `dev` and the commit SHA. A new push to `main` cancels older active runs so they cannot overwrite `latest`.

A Git tag such as `v1.0.0` publishes the same Linux AMD64 image under `v1.0.0` and `1.0.0` after all test jobs pass. The binary reports `1.0.0` and the commit SHA. Release tags must use `vMAJOR.MINOR.PATCH` without leading zeroes. Pull requests and other branch pushes run tests without publishing images.

After the release image publishes, the same job pushes the Helm chart to `oci://ghcr.io/cornfeedhobo/charts/nzb-proxy`. The Git tag supplies the chart version, application version, and default image tag. For example, `v1.0.0` sets all three to `1.0.0`. For public installs, set both GHCR packages to public. See the [Helm install guide](deploy/helm/README.md).

Images include OCI version, revision, source, and license labels. The binary also contains its version and commit. Run `nzb-proxy --version` to print them without loading configuration. Local builds report `dev` and `unknown` by default. CI reads the Go version from `go.mod`.

To embed a version and commit in a manual build:

```sh
go build -ldflags="-X main.version=1.0.0 -X main.commit=$(git rev-parse HEAD)" -o bin/nzb-proxy ./cmd/nzb-proxy
./bin/nzb-proxy --version
```

Docker builds accept the equivalent `VERSION` and `REVISION` build arguments. CI supplies the release version, or `dev` for `main`, and the commit SHA. To print the version of the current main build, run `docker run --rm ghcr.io/cornfeedhobo/nzb-proxy:latest --version`.

After you commit and push the release changes, create and push a new tag:

```sh
git tag -a v1.0.1 -m "Release v1.0.1"
git push origin v1.0.1
```

Use a new tag for each release. Do not move an existing tag. The workflow uses GitHub's `GITHUB_TOKEN` with package write permission for the publishing job. It does not require Docker Hub credentials. Configure package visibility in GHCR for the intended consumers. An image is available only after an eligible workflow run publishes it. Dependabot checks GitHub Actions versions weekly and groups updates into one pull request. See [GitHub's container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).

### Local checks

```sh
go test -race ./...
go vet ./...
go build -o bin/nzb-proxy ./cmd/nzb-proxy
```

Run the end-to-end test from the repository root.

```sh
make e2e
```

The suite runs Prowlarr, Radarr, and the proxy in a temporary kind cluster. It exercises both the Kustomize deployment and the Helm chart. See the [end-to-end test guide](tests/e2e/README.md) for prerequisites and coverage.
