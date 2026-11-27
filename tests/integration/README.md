# Integration test with real applications

From the repository root:

```sh
python3 tests/integration/run.py
```

Requires Python 3.10+, OpenSSL, Docker Engine with Compose v2, and permission to
use Docker. No Python packages or local Go installation are needed. The initial
run needs network access to pull images and build the proxy. Allow several
minutes and enough memory to run Prowlarr and Radarr together.

The runner builds the current proxy source and starts fresh instances of:

- Prowlarr `2.4.0.5391`.
- Radarr `6.2.0.10390`.
- A small Python Newznab provider with a counted HTTPS download endpoint.

Prowlarr and Radarr are real, unmodified application images. Only the indexer is
simulated. The fixture supplies a structurally valid NZB with an intentionally
nonexistent message ID; there is no download client or NNTP connection.

The tested paths are:

```text
Prowlarr application test/sync → Radarr → proxy → Prowlarr Newznab API
Python download clients → proxy → Prowlarr redirect → HTTPS fixture
```

The runner asserts:

1. Prowlarr can validate a Radarr application through the proxy's ID-0 route.
2. Automatic application synchronization writes the proxy URL into Radarr's
   Newznab configuration, and Radarr successfully tests that configuration.
3. Actual Prowlarr search results have both download fields rewritten and signed,
   including a nonempty proxy URL base path.
4. Two searches yield different encrypted wrappers but the same signed GUID
   identity. Sixteen concurrent downloads across those wrappers return exact
   fixture bytes while causing only one provider fetch and one Prowlarr grab.
5. Further searches reuse that entry. Restarting the proxy preserves both the
   cache and signing key: old and newly issued links still use the saved bytes,
   without additional Prowlarr grabs.
6. Wrong credentials and modified signed requests are rejected. An HTML response
   returned as an NZB causes a 502 on each attempt and is fetched only once.
7. A previously issued link still works after Prowlarr and the provider stop.

Each invocation uses a unique Compose project, random test API key, temporary TLS
certificate trusted by the proxy, fresh application databases, and an independent
cache volume. Ports bind only to loopback with dynamically allocated numbers.
There is no connection to existing stacks or real indexers. Containers can make
ordinary background update checks over the network. This requires a local Docker
daemon because the runner uses bind mounts and localhost ports.

The runner prints `PASS` checkpoints, exits nonzero on failure, prints recent
container logs for diagnosis, and removes its containers, network, volumes, and
temporary configuration in a `finally` block. Downloaded images and build cache
remain available for subsequent runs. If the process is forcibly killed, use the
`nzb-proxy-test-...` project name printed in its output to identify its resources;
do not clean up unrelated Docker projects.

GitHub Actions runs the same command in a separate integration job. Changing the
application versions in `compose.yaml` requires rerunning the test; the script
uses each application's live configuration schema and fails if required fields
change.

## Boundaries

The harness tests connectivity, sync, search, and redirects with the Prowlarr and
Radarr versions listed above. The Python runner sends the download requests.
Radarr movie grabs, uploads to a download client, Sonarr, and other application
builds need separate tests. Live deployments also need separate tests for provider
GUID stability, TLS, CDN redirects, and Usenet downloads. The Go tests cover cache
expiration and additional error cases.
