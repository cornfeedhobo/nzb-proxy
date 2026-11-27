# NZB proxy

NZB proxy caches NZB files downloaded through Prowlarr,
allowing Radarr and Sonarr to reuse a cached file instead of downloading it again.

The proxy forwards searches to Prowlarr and rewrites download links to pass through the cache.
It follows redirects, validates each NZB, and saves the file.
Concurrent requests for the same release share one download.
Cached files survive restarts and expire after seven days by default.

The cache does not store Usenet articles or change how the download client retrieves them.

This is useful if you are running multiple instances of Radarr and Sonarr.

## Run locally

```sh
export NZB_PROXY_UPSTREAM_URL=http://prowlarr:9696
export NZB_PROXY_PUBLIC_URL=http://nzb-proxy:8080
export NZB_PROXY_API_KEY='your-prowlarr-api-key'
export NZB_PROXY_ALLOWED_HOSTS=indexer.example
go run ./cmd/nzb-proxy
```

Replace the example values with your own. See [Configuration](#configuration) for the required values and download host rules.

## Run in Docker

Copy `.env.example` to `.env`. Set the Prowlarr API key, URLs, and allowed hosts in `.env`. The example [Compose file](compose.yaml) passes the five variables listed in [.env.example](.env.example) to the container. To pass another variable from `.env`, add it under `environment` for the service, for example `NZB_PROXY_FETCH_TIMEOUT: ${NZB_PROXY_FETCH_TIMEOUT:-60s}`.

Attach the service in `compose.yaml` to the network shared by Prowlarr, Radarr, and Sonarr. The example builds the image from source. For the published Linux AMD64 image, replace `build: .` with `image: ghcr.io/cornfeedhobo/nzb-proxy:latest`. See [Releases and image versions](#releases-and-image-versions) for available tag formats.

Start the proxy from the repository root.

```sh
docker compose up -d --build
```

The container runs as UID and GID 10001. It stores cache files in `/data`. The example uses a named volume. If you use a bind mount, make sure that this user can write to it. The host port binds to loopback by default. The `/healthz` endpoint reports whether the process is running and does not require authentication.

## Connect Prowlarr and the applications

For each Radarr and Sonarr application in Prowlarr, set **Prowlarr Server** to the proxy's public URL. Its API field is `prowlarrUrl`. Keep **Application Server** pointed directly at Radarr or Sonarr. Then test and sync the application.

The synced indexer URLs use the proxy address and indexer IDs, such as `/1/` and `/2/`. The proxy also supports indexer ID `0` for Prowlarr's application test.

The proxy exposes `/{id}/api` and `/{id}/download` under its public URL. It does not expose Prowlarr's alternate `/api/v1/indexer/...` routes or its administration API. Open Prowlarr directly for its UI and administration API.

Before you use live downloads, make sure that search-result links point to the proxy. Queued links that point directly to Prowlarr bypass the cache.

## Configuration

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `NZB_PROXY_UPSTREAM_URL` | Required | Prowlarr HTTP(S) URL, including any base path |
| `NZB_PROXY_PUBLIC_URL` | Required | Proxy URL for clients, including any base path |
| `NZB_PROXY_API_KEY` | Required | Prowlarr API key, required for all gateway requests |
| `NZB_PROXY_ALLOWED_HOSTS` | Empty | Comma-separated hostnames allowed for HTTPS downloads and redirects |
| `NZB_PROXY_LISTEN` | `:8080` | HTTP listen address |
| `NZB_PROXY_SIGNING_KEY_FILE` | Unset | Path to a mounted file containing 32 raw key bytes. The proxy reads it at startup. |
| `NZB_PROXY_CACHE_DIR` | `./data` | Persistent cache directory |
| `NZB_PROXY_CACHE_TTL` | `168h` | Retention for new entries, such as `24h` or `168h` |
| `NZB_PROXY_FETCH_TIMEOUT` | `60s` | Timeout for an upstream fetch |
| `NZB_PROXY_MAX_NZB_BYTES` | `33554432` | Maximum decompressed NZB size, in bytes |

These are the binary defaults. The Docker example sets its own cache directory and example hosts.

List every indexer and CDN download host in `NZB_PROXY_ALLOWED_HOSTS`. Use exact hostnames without a scheme, path, port, or wildcard. External downloads require HTTPS. The configured Prowlarr origin can use HTTP. Configuration and request errors do not intentionally expose download URLs or API keys.

Failed downloads log the indexer ID and the failure. The log includes the status for an upstream HTTP error. A blocked redirect includes its hostname and scheme. Download URLs and response bodies are not logged.

If an upstream returns HTTP 429, the proxy returns HTTP 429 to the client. It forwards a valid `Retry-After` header when present. Other download failures return HTTP 502.

## Cache behavior

- Search results include a signed release identity. A cache hit can skip Prowlarr and the indexer, even when the download URL changes. This depends on the indexer providing a stable RSS GUID (release identifier). See [how the proxy identifies a release](docs/prowlarr-compatibility.md#cache-identity).
- For an unsigned download link, the proxy checks the wrapper URL first. On a miss, it resolves Prowlarr's redirect and checks the destination URL. Resolving a redirect can count as a Prowlarr grab. A changed destination token can cause a cache miss.
- Cache entries are scoped to the Prowlarr instance, indexer, and API key. Releases from different indexers use separate entries. The proxy does not use titles as identities.
- Retention starts when the proxy saves an entry. Reads do not extend it. A new retention value applies to new entries. Expiry, corruption, or deletion can cause another download.
- One Linux process can own a cache directory. A file lock enforces this limit. Independent replicas cannot share in-progress downloads.
- Set `NZB_PROXY_SIGNING_KEY_FILE` to keep signed links valid when replacing the cache. Mount the key file read-only with mode `0440` and give the process group read access. By default, the proxy creates `.signing-key` in the cache directory. Preserve that key across restarts so existing signed links remain valid. The signing key is separate from the Prowlarr API key.
- Entries are JSON files containing metadata and base64-encoded NZB bytes. Cleanup runs at startup and hourly. The cache has no disk quota or least-recently-used eviction.
- The proxy does not serve invalid NZBs. It records an invalid response for 15 minutes to prevent repeated downloads. This record survives restarts. A shorter cache retention also shortens the record's lifetime.
- Oversized and unsuccessful responses do not get a failure record. Filesystem errors fail the request instead of bypassing the cache. An interrupted or failed request can require another download.
- NZB validation supports UTF-8 and ISO-8859-1. The proxy preserves the original bytes.
- The gateway accepts GET and rejects HEAD so health checks cannot grab an NZB. It forwards searches without caching their responses.

## Development

### Releases and image versions

After both test jobs pass, pushes and merges to `main` publish `ghcr.io/cornfeedhobo/nzb-proxy:latest` and `ghcr.io/cornfeedhobo/nzb-proxy:sha-<full-commit-sha>`. These binaries report `dev` and the commit SHA. A new push to `main` cancels older active runs so they cannot overwrite `latest`.

A Git tag such as `v0.1.0` publishes `ghcr.io/cornfeedhobo/nzb-proxy:0.1.0` after both test jobs pass. It does not change `latest` or the development tags. Release tags must use `vMAJOR.MINOR.PATCH` without leading zeroes. The workflow publishes Linux AMD64 images. Pull requests and other branch pushes run tests without publishing images.

Images include OCI version, revision, source, and license labels. The binary also contains its version and commit. Run `nzb-proxy --version` to print them without loading configuration. Local builds report `dev` and `unknown` by default. The `.go-version` file selects the Go toolchain, not the application version.

To embed a version and commit in a manual build:

```sh
go build -ldflags="-X main.version=0.1.0 -X main.commit=$(git rev-parse HEAD)" -o bin/nzb-proxy ./cmd/nzb-proxy
./bin/nzb-proxy --version
```

Docker builds accept the equivalent `VERSION` and `REVISION` build arguments. CI supplies the release version, or `dev` for `main`, and the commit SHA. A published image also supports `docker run --rm ghcr.io/cornfeedhobo/nzb-proxy:0.1.0 --version`.

After you commit and push the release changes, create and push a new tag:

```sh
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

Use a new tag for each release. Do not move an existing tag. The workflow uses GitHub's `GITHUB_TOKEN` with package write permission for the publishing job. It does not require Docker Hub credentials. Configure package visibility in GHCR for the intended consumers. An image is available only after an eligible workflow run publishes it. Dependabot checks GitHub Actions versions weekly and groups updates into one pull request. See [GitHub's container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).

### Local checks

```sh
go test -race ./...
go vet ./...
go build -o bin/nzb-proxy ./cmd/nzb-proxy
```

The Go tests use simulated services. The integration harness uses real Prowlarr and Radarr containers with a controlled HTTPS Newznab provider:

```sh
python3 tests/integration/run.py
```

The harness requires Docker Compose, Python 3.10 or newer, and OpenSSL. It tests application sync, link rewriting, cache reuse, restart persistence, and failure handling. It removes its test containers and volumes afterward. It needs no indexer credentials or live downloads. See the [integration test guide](tests/integration/README.md) for details and limits.
