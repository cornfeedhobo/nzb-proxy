# Prowlarr compatibility research

Researched 2026-10-03 against the public `v2.4.0.5391` source tag, matching the recorded Lakshmi image version. This verifies upstream implementation, not a live deployment trace. No indexer searches or NZB grabs were performed for this research.

## Routes and feed fields

[NewznabController](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/Prowlarr.Api.V1/Indexers/NewznabController.cs) exposes:

| Purpose | Route | Alternate route |
| --- | --- | --- |
| Newznab capabilities/search | `/{id}/api?t=...` | `/api/v1/indexer/{id}/newznab?t=...` |
| NZB retrieval | `/{id}/download` | `/api/v1/indexer/{id}/download` |

The configured Prowlarr URL base prefixes these routes where applicable. Download requests require both `link` and `file` query parameters. Generated links also include `apikey`. For Usenet, the download controller **always returns a permanent redirect (301)** after recording the grab. Disabling an indexer's redirect setting cannot make this version proxy Usenet bytes.

Search functions include `search`, `tvsearch`, `movie`, `music`, and `book`; `caps` returns capabilities. The controller returns XML, including XML errors; it does not select a JSON response based on a Newznab query option. Numeric indexer ID `0` is special: capabilities and search responses are synthetic fixtures used in application connectivity tests. The proxy must permit `/0/api`.

[NewznabResults](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/IndexerSearch/NewznabResults.cs) emits each RSS item with:

- `guid`: the release GUID, unchanged by the download-link wrapper.
- `link`: the Prowlarr download URL.
- `enclosure/@url`: the same download URL, with `type="application/x-nzb"` for Usenet.
- `prowlarrindexer/@id`: the Prowlarr indexer ID.

Rewrite both download fields; preserve GUIDs, comments/info links, and unrelated metadata. The synthetic ID-0 test release points to `https://prowlarr.com`, not a real download route.

## Why the encrypted link is not a release identifier

[DownloadMappingService](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Download/DownloadMappingService.cs) creates links of this shape:

```text
/{indexerId}/download?apikey=REDACTED&link=ENCRYPTED_URL&file=TITLE
```

The `link` value is a base64url-encoded protected string. [ProtectionService](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Security/ProtectionService.cs) encrypts with a fresh AES initialization vector and embeds that IV in its result. Rewrapping the exact same upstream URL therefore produces different `link` values. Hashing the wrapper URL alone does not deduplicate requests coming from separate searches.

Recommended primary identity: configured Prowlarr/account scope + indexer ID + RSS item GUID. Authenticate the identity in rewritten download URLs, binding it to the download request, so callers cannot substitute an unrelated upstream link into an existing cache identity. Do not trust an unsigned caller-provided GUID. Keep the original wrapped URL available for a cache miss. Preserve the proxy signing secret across restarts.

This assumes the indexer supplies a stable, unique GUID. [RssParser.GetGuid](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Indexers/RssParser.cs) uses the feed's GUID and generates a random GUID if it is absent. Thus missing or unstable provider GUIDs can still cause misses. Representative sanitized deployed feeds remain to be validated. GUIDs are not guaranteed to be bare Newznab release IDs; use the full value as opaque identity, without title matching or cross-indexer merging.

Resolving the Prowlarr redirect before cache lookup is a possible fallback identity source: key the full resulting upstream URL conservatively, retaining credential/account parameters. It has an operational cost. [DownloadService.RecordRedirect](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Download/DownloadService.cs) emits a successful download event; [HistoryService](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/History/HistoryService.cs) records it as `ReleaseGrabbed`; [IndexerLimitService](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Indexers/IndexerLimitService.cs) counts these events against the locally configured grab limit. The controller also checks disabled/blocked state and grab limits before redirecting. Resolving on every cache hit would inflate Prowlarr history and could prevent serving cached files when a local limit is reached. Prefer cache lookup before contacting Prowlarr for signed feed-derived identities.

## Application sync and deployment configuration

Prowlarr application settings expose **Prowlarr Server** (`ProwlarrUrl`), described as the URL as the target application sees it. Set this to the proxy address separately for each participating Radarr/Sonarr application. Keep each **Radarr Server** or **Sonarr Server** pointing directly at its existing application.

[Radarr](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Radarr/Radarr.cs) and [Sonarr](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Sonarr/Sonarr.cs) construct synchronized Newznab settings with `baseUrl = ProwlarrUrl + /{indexerId}/`, `apiPath = /api`, and the Prowlarr API key. This makes future sync preserve the proxy placement. Manually editing only the app's indexer URL could be overwritten on later sync.

The application test builds a synthetic indexer with ID `0`. [RadarrV3Proxy](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Radarr/RadarrV3Proxy.cs) and [SonarrV3Proxy](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Sonarr/SonarrV3Proxy.cs) send its configuration directly to the application's `/api/v3/indexer/test` endpoint. The application then tests Newznab via the configured proxy address. There is no requirement to expose Prowlarr's administrative API through this proxy for this synchronization workflow.

[RequestExtensions.GetServerUrl](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/Prowlarr.Http/Extensions/RequestExtensions.cs) builds generated link origins from the request Host and scheme, with support for `X-Forwarded-Proto`. Explicit response rewriting to a configured public proxy URL avoids relying solely on forwarded-header reconstruction. Treat the configured URL as trusted configuration rather than accepting arbitrary Host headers for link generation.

## Remaining integration checks

The [Docker integration harness](../tests/integration/README.md) now verifies
application connectivity and automatic Radarr sync, a nonempty proxy URL base,
real Prowlarr encrypted-link rotation, concurrent downloads, restart persistence,
and authentication/signature enforcement with a controlled provider. It uses
Prowlarr 2.4.0.5391 and stock Radarr 6.2.0.10390. The following deployment and
broader fixture checks remain relevant:

- Inspect sanitized feeds from the three configured indexers to confirm stable GUIDs; avoid real NZB retrieval for this check.
- Test application connectivity and synchronization against a staging instance before changing Lakshmi.
- Verify URL-base handling, both route aliases, XML namespaces and escaped query strings with fixtures.
- Confirm multiple different encrypted links with the same GUID lead to one upstream NZB fetch, including concurrent requests and process restarts.
- Verify signed identities cannot be altered or reused with different upstream requests, and API authentication is enforced before returning cached bytes.

These are research findings and implementation recommendations. Deployment remains a separate step.
