# NZB proxy

A caching proxy written in Go for NZB retrieval through Prowlarr. Multiple Radarr/Sonarr instances can reuse one saved NZB during a configurable retention window.

The cache stores NZB manifests, not Usenet article data. The existing nzbdav → nntp-proxy → Usenet path remains separate.

The proxy forwards Newznab searches, rewrites download links to itself, follows download redirects internally, and saves validated NZB files. Concurrent requests for one cache identity share a fetch. Cache files survive restarts, with a default retention of seven days measured from insertion.

## Run locally

Requires Go 1.26 or newer and Linux. The repository pins Go 1.26.4 for development. There are no third-party Go dependencies.

```sh
export NZB_PROXY_UPSTREAM_URL=http://prowlarr:9696
export NZB_PROXY_PUBLIC_URL=http://nzb-proxy:8080
export NZB_PROXY_API_KEY='your-prowlarr-api-key'
export NZB_PROXY_ALLOWED_HOSTS=api.nzb.life,api.nzbgeek.info,nzbfinder.ws
go run ./cmd/nzb-proxy
```

Use addresses reachable from their respective services: the proxy must reach the upstream URL, and Radarr/Sonarr must reach the public URL. The hostnames above are examples for a shared container network, not public DNS addresses.

## Run in Docker

Copy `.env.example` to `.env`, supply your Prowlarr API key, and adjust the URLs and allowed hosts. Attach the service in `compose.yaml` to the container network used by Prowlarr and Radarr/Sonarr before starting it; the example's default Compose network does not automatically join an existing stack.

```sh
docker compose up -d --build
```

The image runs as UID/GID 10001 and stores cache files in `/data`. The example uses a named volume; a bind mount must be writable by that user. `/healthz` is an unauthenticated liveness endpoint. The host port binds to loopback by default.

## Connect Prowlarr and the applications

In each Prowlarr application entry for Radarr/Sonarr, set **Prowlarr Server** (the API field is `prowlarrUrl`) to the proxy's public URL. Keep **Application Server** pointing directly at Radarr/Sonarr. Then test and sync the application. The synced indexer addresses should become `http://nzb-proxy:8080/1/`, `/2/`, and so on. The proxy also supports indexer ID `0`, used during Prowlarr's application validation.

The gateway exposes `/{id}/api` and `/{id}/download`, beneath any configured public base path. Prowlarr's alternate `/api/v1/indexer/...` routes are not exposed. Continue opening Prowlarr directly for its UI and administrative API. The nzbdav download-client configuration and NNTP path do not change.

Before using live downloads, verify that search-result download links use the proxy's public URL. Existing queued links pointing directly at Prowlarr bypass this service.

## Configuration

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `NZB_PROXY_UPSTREAM_URL` | Required | Internal Prowlarr HTTP(S) URL, including any base path |
| `NZB_PROXY_PUBLIC_URL` | Required | URL clients use to reach this proxy, including any base path |
| `NZB_PROXY_API_KEY` | Required | Prowlarr API key; required on gateway requests, including cache hits |
| `NZB_PROXY_ALLOWED_HOSTS` | Empty | Comma-separated exact indexer/CDN hostnames permitted for HTTPS downloads and redirects |
| `NZB_PROXY_LISTEN` | `:8080` | HTTP listen address |
| `NZB_PROXY_CACHE_DIR` | `./data` | Persistent cache directory |
| `NZB_PROXY_CACHE_TTL` | `168h` | Retention for newly written entries, such as `24h` or `168h` |
| `NZB_PROXY_FETCH_TIMEOUT` | `60s` | Timeout for an upstream fetch |
| `NZB_PROXY_MAX_NZB_BYTES` | `33554432` | Maximum decompressed NZB size, in bytes |

Add any legitimate CDN redirect destinations to the allowed hosts explicitly. Hostnames have no scheme, path, port, or wildcard. External downloads require HTTPS; the configured Prowlarr origin can use HTTP. Configuration and request errors do not intentionally expose download URLs or API keys.

## Cache behavior and limitations

- Signed release identities in rewritten RSS links allow cache hits to skip both Prowlarr and the indexer, even when Prowlarr generates a different encrypted download URL for the same release. This depends on the indexer supplying a stable RSS GUID.
- Unsigned download links first check a cache identity based on the wrapper URL. On a miss, they resolve Prowlarr's redirect and check a second identity based on the upstream URL. A changed wrapper can therefore reuse the saved NZB, but resolving it can still count as a grab in Prowlarr. Changing indexer URL tokens can cause misses.
- Cache identities are scoped to the configured Prowlarr instance, indexer, and API key. Releases on different indexers are separate entries; titles are not used as identities.
- Retention is fixed at write time, not extended by reads. Changing the configured TTL affects new entries. Expiration, corruption, or manual deletion can result in another indexer fetch.
- One Linux process owns each cache directory, enforced by a file lock. Do not deploy independent replicas expecting shared in-flight deduplication.
- A private signing key is generated in the cache volume as `.signing-key`. Preserve it across restarts so previously issued download links remain valid. It is separate from the Prowlarr API key shared with clients.
- Entries are atomic JSON records containing metadata and base64-encoded NZB bytes. Cleanup runs at startup and hourly. There is no total disk quota or LRU eviction yet.
- Invalid, oversized, or unsuccessful downloads are not cached. Filesystem errors fail the request instead of silently bypassing the cache. Interrupted or failed upstream requests can require another attempt; this is not a lifetime exactly-once guarantee.
- GET is supported; HEAD is rejected so health checks cannot accidentally grab an NZB. Search responses are forwarded rather than cached.

## Development

### Releases and image versions

Successful pushes and merges to `main` publish `ghcr.io/cornfeedhobo/nzb-proxy:latest`
and `ghcr.io/cornfeedhobo/nzb-proxy:sha-<full-commit-sha>`, after both test jobs pass.
These binaries report `dev` plus the commit SHA. New `main` pushes cancel older
in-progress runs so they do not later overwrite `latest`.

Git release tags are the release version source. Pushing `v0.1.0`, for example,
runs both test jobs and then publishes `ghcr.io/cornfeedhobo/nzb-proxy:0.1.0`.
Release builds leave `latest` and the development commit tags alone. The image records its
version, source repository, and commit in OCI labels. The same version and commit
are embedded in the Go binary at build time and printed by `nzb-proxy --version`,
without requiring service configuration or starting the server. There is no
separate `VERSION` file. `.go-version` selects the Go toolchain, not the application version.

For example, a `v0.1.0` release reports `nzb-proxy 0.1.0 (commit <full-commit-sha>)`.
Ordinary local builds report `nzb-proxy dev (commit unknown)`. To embed a version
in a manual build:

```sh
go build -ldflags="-X main.version=0.1.0 -X main.commit=$(git rev-parse HEAD)" -o bin/nzb-proxy ./cmd/nzb-proxy
./bin/nzb-proxy --version
```

Docker builds accept equivalent `VERSION` and `REVISION` build arguments. CI
supplies the release version (or `dev` for `main`) and commit; a published image supports
`docker run --rm ghcr.io/cornfeedhobo/nzb-proxy:0.1.0 --version`.

Release tags must use `vMAJOR.MINOR.PATCH`, with no leading zeroes. The initial
publishing workflow supports Linux AMD64. Use a new version tag for each release
rather than moving an existing tag. Other branch pushes and pull requests run
tests only.

After the desired changes are committed and pushed, create and push a release tag:

```sh
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

The workflow authenticates to GHCR using GitHub's `GITHUB_TOKEN`, with package
write permission limited to the publishing job; no Docker Hub credentials are
needed. See [GitHub's container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).
Package visibility/access must be configured in GHCR for the intended consumers.
No image is available until an eligible workflow run publishes successfully.
Dependabot checks GitHub Actions versions weekly and groups updates into one PR.

### Local checks

```sh
go test -race ./...
go vet ./...
go build -o bin/nzb-proxy ./cmd/nzb-proxy
```

The Go tests use local simulated upstream services. To test with real Prowlarr and Radarr containers plus a controlled HTTPS Newznab provider:

```sh
python3 tests/integration/run.py
```

Requires Docker Compose, Python 3.10+, and OpenSSL. The harness tests application sync, link rewriting, concurrent cache reuse, restart persistence, and failure handling, then removes its test containers and volumes. It needs no indexer credentials or live grabs. See the [integration test guide](tests/integration/README.md) for assertions and coverage limits.

See [project context](docs/project-context.md) for decisions and deployment observations, and [Prowlarr compatibility](docs/prowlarr-compatibility.md) for integration findings.

Status: initial implementation; not deployed to Lakshmi or verified against live indexer downloads.
