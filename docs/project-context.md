# NZB proxy project context

Recorded on 2026-10-03 from the initial design discussion and read-only inspection of Lakshmi. This document is the continuity reference for future project work. Deployment observations are a dated snapshot, not guarantees about future configuration.

## Purpose and agreed scope

The user runs multiple download workflows through Prowlarr. They sometimes retrieve the same NZB repeatedly, which their index providers discourage. The project should fetch an NZB once, retain its bytes locally, and serve subsequent requests from that copy.

The user confirmed this understanding and the architectural direction below. Cache retention is configurable; the user considers a day or a week sufficient. Seven days is the implemented initial default, subject to later tuning.

The promise is reuse during the retention window, not permanent lifetime deduplication. An expired or removed entry may require another upstream download. Transport failures and crashes also need explicit retry semantics; an absolute exactly-once guarantee has not been established.

## Required behavior

- On a cache miss, fetch the NZB and persist a valid complete response before treating the entry as reusable.
- On a cache hit, return the saved NZB without fetching it again from the indexer.
- When requests for the same entry arrive simultaneously, share one in-progress upstream fetch.
- Preserve cache entries across process/container restarts.
- Support configurable retention with an initial default of seven days.
- Validate downloaded data so HTML error pages, API errors, and incomplete responses do not become cached NZBs.
- Keep Prowlarr responsible for indexer integration and search.
- Leave the Usenet article retrieval path independent of the NZB cache.

## Existing integration and evidence

The user described two paths:

- NZB discovery/retrieval: Radarr/Sonarr → Prowlarr.
- Usenet retrieval: Radarr/Sonarr → nzbdav → nntp-proxy → Usenet.

Upstream source clarifies the standard handoff: Radarr/Sonarr retrieve the NZB bytes, then upload the file to the SABnzbd-compatible download client using `mode=addfile`. nzbdav's setup guide describes receiving that NZB file. Thus a cache placed only between Radarr/Sonarr and nzbdav would act after the indexer fetch and would not prevent repeated indexer downloads.

Sources inspected:

- [Radarr UsenetClientBase](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Download/UsenetClientBase.cs): retrieves the release download URL, allows redirects, validates bytes, and hands them to the client adapter.
- [Radarr SabnzbdProxy](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdProxy.cs): posts the NZB with `addfile` as a multipart upload.
- [Sonarr Sabnzbd](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs) and [SabnzbdProxy](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Download/Clients/Sabnzbd/SabnzbdProxy.cs): hand off NZB bytes and upload using `addfile`.
- [nzbdav setup guide](https://github.com/nzbdav-dev/nzbdav/blob/main/docs/setup-guide.md): describes Radarr sending an NZB file to nzbdav.
- [Prowlarr IndexerController](https://github.com/Prowlarr/Prowlarr/blob/develop/src/Prowlarr.Api.V1/Indexers/IndexerController.cs): current development source requires redirects for Usenet indexers that support them.
- [Newznab API specification maintained by nZEDb](https://github.com/nZEDb/nZEDb/blob/0.x/docs/newznab_api_specification.txt): documents release identifiers and download links.

These are moving upstream branches, not pinned source for the deployed images. The deployed Radarr/Sonarr images are customized. Configuration and upstream code support the inferred flow below, but individual deployed upload requests were not traced: recent log samples contained no `mode=addfile` or `mode=addurl` markers.

Expected current retrieval sequence:

1. Radarr/Sonarr obtain release information through Prowlarr.
2. They request a download and follow the Prowlarr redirect to the indexer.
3. They retrieve the NZB bytes from the indexer.
4. They upload those bytes to nzbdav.
5. nzbdav retrieves Usenet article data through nntp-proxy as needed.

## Lakshmi observations

The user authorized inspection of Lakshmi for this research. Read-only inspection made no service or configuration changes and triggered no NZB downloads.

- SSH alias: `lakshmi.wepayforthis.com`; reported hostname: `lakshmi.fuzzlabs.org`.
- Prowlarr image: `ghcr.io/home-operations/prowlarr:2.4.0.5391`.
- Radarr images: `localhost/radarr-strm:6.2.0.10390-4`.
- Sonarr images: `localhost/sonarr-strm:4.0.17.2953-2`.
- nzbdav images: `nzbdav/nzbdav:0.6.4`.
- Multiple stacks exist, including hd-efficient, uhd-efficient, hd-balanced, uhd-balanced, and hd-compact, plus main Radarr/Sonarr instances.
- The sampled hd-efficient Radarr and Sonarr configuration uses a `Sabnzbd` client named `nzbdav` at `hd-efficient-nzbdav:3000`, without TLS.
- Both sampled instances have Newznab indexers pointed at `http://prowlarr:9696/1/`, `/2/`, and `/3/`.
- Prowlarr maps these to Nzb.life (`https://api.nzb.life`), NZBgeek (`https://api.nzbgeek.info`), and NZBFinder (`https://nzbfinder.ws`). All three have redirects enabled.

Configuration locations inspected:

- `/var/servarr/nzbarr/hd-efficient/radarr/config/radarr.db`
- `/var/servarr/nzbarr/hd-efficient/sonarr/config/sonarr.db`
- `/var/servarr/servarr/prowlarr/config/prowlarr.db`

SQLite connections used read-only mode and output was limited to selected non-secret fields. Do not record API keys, passwords, or credential-bearing download URLs in this repository.

For future inspection, explicit `ssh -F /home/cornfeedhobo/.ssh/config` avoided a local system SSH configuration ownership error. The restricted execution environment could not resolve the server; the authorized network-enabled execution succeeded. Agent forwarding was disabled.

## Accepted architectural direction

Place a shared application-aware proxy on the NZB retrieval path used by Radarr/Sonarr, in front of Prowlarr from their perspective.

- Pass search/API requests through to Prowlarr.
- Ensure download links returned to clients route through the proxy, rewriting relevant response fields when needed.
- For NZB downloads, follow redirects internally and return the actual NZB bytes rather than redirecting the client around the cache.
- Store and reuse successful NZB responses across participating application instances.

A generic reverse proxy that merely caches Prowlarr redirect responses is insufficient: each client could still follow the redirect and download the NZB again from the indexer. A proxy only on Prowlarr's outbound traffic also cannot catch requests that redirected clients make directly.

An initial implementation now provides this architecture. Pinned Prowlarr 2.4.0.5391 source establishes the route and application-sync behavior; see [compatibility research](prowlarr-compatibility.md). Go tests use simulated services; a Docker integration harness now also exercises real Prowlarr and Radarr with a controlled Newznab provider. No deployment or live indexer validation has occurred.

## Language decision

The user selected Go on 2026-10-03. The rationale is straightforward HTTP handling, concurrency, and deployment for a focused network service.

The implementation uses Go's standard HTTP library and no third-party dependencies. A small in-process flight map shares cache lookups and fetches for each identity; request cancellation does not cancel a download needed by another client. The service targets Go 1.26 and Linux, with Go 1.26.4 pinned for development.

Rust was mentioned as a possible future rewrite, not a planned milestone or a requirement for the initial implementation.

## Cache identity

Pinned source revealed that Prowlarr encrypts the upstream URL with a random IV each time it wraps a download link. The opaque `link` parameter is therefore not stable across searches, even for an identical upstream URL. RSS item GUIDs remain available separately.

The implemented primary identity is configured Prowlarr URL + indexer ID + API-key scope + hash of the full RSS GUID. Feed rewriting signs the GUID identity and exact download request with an independent secret, and rewrites item `link` and `enclosure/@url` to the public proxy URL. The proxy validates the signature before serving a signed cache hit, without contacting Prowlarr or the indexer. A stable, unique provider GUID is an integration assumption still to be checked against sanitized deployed feeds. Prowlarr generates random GUIDs if a provider omits them.

Unsigned download requests first use a conservative wrapper-URL identity. On a miss, the proxy requests the Prowlarr redirect and checks a second cache identity based on its resolved destination URL, scoped to the Prowlarr instance, indexer route, and API key. Different encrypted wrappers can therefore share the resolved NZB, although each new wrapper can record a grab in Prowlarr and encounter its grab limits. Changed upstream tokens can still produce misses. Filename/title equality is not used to identify a release, and distinct indexers remain separate.

Cross-indexer content deduplication is outside the initial scope. Hashing bytes after downloading cannot by itself prevent the first fetch of an equivalent item from another indexer.

## Implemented operating decisions

- Keep `cmd/nzb-proxy/main.go` as the small executable entry point. `internal/app` owns service startup, shutdown, and HTTP wiring; `internal/state` owns directory locking and persistent signing keys, with tests beside those implementations.
- The gateway accepts authenticated GET requests at `/{id}/api` and `/{id}/download`, beneath the configured public base path. ID `0` is supported for application connectivity tests. Prowlarr's alternate `/api/v1/indexer/...` routes and administrative UI/API are not exposed. `t=get` is not a supported Prowlarr Newznab operation despite the gateway's download classification for it.
- Each Prowlarr application's **Prowlarr Server** setting should point at the proxy public URL; its **Application Server** setting remains direct. Pinned source confirms this sets the synchronized Newznab base URL. This configuration has not been applied to Lakshmi.
- API authentication is required even on cache hits. External download destinations require HTTPS and an exact hostname allowlist. The configured Prowlarr origin may use HTTP. Redirect handling removes sensitive request headers; no raw download URLs or credentials should be logged.
- `cmd/nzb-proxy` supplies environment-based settings documented in [README](../README.md), with defaults of seven days retention, a 60-second fetch timeout, and a 32 MiB decompressed NZB limit. Search-response bodies also have a 32 MiB bound. `/healthz` is a separate unauthenticated liveness endpoint.
- Cache entries are atomic JSON files containing metadata and base64-encoded NZB bytes. Files and their directory are synced before a write reports success. Expired or structurally corrupt records are misses; ordinary filesystem failures fail the request. Retention begins with the original entry insertion, without extending it on reads. Alias records inherit the original expiration deadline; writing an alias cannot renew the NZB's retention window.
- A 32-byte private signing key is automatically persisted as `.signing-key` with mode 0600 under the cache directory. Preserve it to keep previously issued signed links valid. Corrupt existing keys are rejected rather than replaced. Prowlarr API-key rotation also changes cache scope.
- One process owns a cache directory, enforced with a Linux file lock. Independent replicas do not coordinate. Cleanup runs at startup and hourly. No disk quota or LRU eviction is implemented; resolved and wrapper/GUID identities can store duplicate copies of the same NZB bytes.
- Downloads require HTTP 200 and a complete XML NZB document containing nonempty message segments under `nzb/file/segments`. This is structural validation, not complete NZB schema or Usenet availability validation. Invalid, oversized, or unsuccessful responses are not cached. Failed or interrupted attempts can require a new fetch.
- Docker and Compose examples are provided. Existing container networks and allowlisted CDN hosts must be configured for the actual deployment.

## Remaining integration work

1. Inspect sanitized feeds from the configured indexers to verify stable GUIDs and download-link origins without initiating NZB grabs.
2. Repeat application connectivity and synchronization checks against the customized deployment images before rollout. The isolated harness verifies stock Prowlarr/Radarr connectivity, sync, signed links, and a proxy URL base path.
3. Verify the customized Radarr/Sonarr handoff against deployed source or a controlled trace if needed.
4. Validate the deployed chain with a deliberately selected NZB only when authorized; fixture success does not prove live integration.
5. Decide whether operational demand warrants disk quotas, metrics, or richer error propagation. Upstream download errors currently return a generic 502.

Implementation is present and covered by local fixtures. No Lakshmi service configuration has been changed, and deployment remains a separate step.

## Real-application integration harness

Added and validated on 2026-10-03 (US/Central): `python3 tests/integration/run.py`.
See the [integration test guide](../tests/integration/README.md). The same command
is configured as a separate GitHub Actions job; remote CI execution has not been
observed in this session.

- Runs actual Prowlarr 2.4.0.5391 and upstream Radarr 6.2.0.10390 images, the proxy
  built from this checkout, and a controlled Newznab provider serving HTTPS NZBs.
- Verified Prowlarr application validation through ID 0, automatic Radarr indexer
  synchronization, and Radarr's test of the synchronized proxy URL with `/cache`
  as its public base path.
- Verified different encrypted Prowlarr links for the same GUID, rewriting of
  both RSS download fields, and sixteen concurrent downloads producing exactly
  one provider payload fetch and one Prowlarr `releaseGrabbed` history entry.
- Verified further searches and a proxy restart reuse the persisted bytes and
  signing key without another grab, invalid credentials/signatures are rejected,
  HTML errors are not cached, and cached downloads work with both upstreams stopped.
- Uses fresh volumes, temporary credentials/certificates, ephemeral loopback
  ports, and automatic cleanup. No real indexers, download clients, or existing
  deployment configuration are used. Ordinary app background update checks may
  access the network.

The Python runner drives downloads. This does not validate a Radarr movie grab,
the nzbdav upload handoff, Sonarr, custom deployment builds, or live provider GUIDs.
These boundaries are intentional; the provider fixture makes upstream fetch
counts deterministic without spending real grabs.

## Initial implementation validation

Validated on 2026-10-03:

- `go test -race ./...`, `go vet ./...`, and the Go binary build passed. Formatting is clean.
- A separate binary smoke test used a simulated Prowlarr redirect and NZB endpoint. Sixteen concurrent requests, subsequent searches with randomized wrapper links, and a process restart produced three searches but only one Prowlarr download redirect and one NZB payload fetch in total. The same previously issued signed URL remained valid after restart.
- `docker build -t nzb-proxy:dev .` and Compose configuration validation passed. The container started as UID 10001 with networking disabled, created its persistent-state files, and returned `ok` from its local health endpoint. The temporary smoke-test container was removed.
- Regression tests cover malformed/oversized NZBs, API authentication, redirect credential stripping, destination restrictions, URL base paths, XML entities/CDATA/namespaces, ID-0 connectivity fixtures, request cancellation, signature tampering, indexer isolation, disk failures, cache corruption, and inherited expiration.

These checks contacted no live indexers. The local development image exists; nothing was published or deployed.
