# Prowlarr compatibility

This document explains the Prowlarr behavior that affects the proxy's design. The source links refer to Prowlarr `v2.4.0.5391`. Other versions may behave differently.

## Routes and search results

Prowlarr's [Newznab controller](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/Prowlarr.Api.V1/Indexers/NewznabController.cs) provides search and download routes:

| Purpose | Route | Alternate Prowlarr route |
| --- | --- | --- |
| Newznab capabilities and search | `/{id}/api?t=...` | `/api/v1/indexer/{id}/newznab?t=...` |
| NZB download | `/{id}/download` | `/api/v1/indexer/{id}/download` |

The proxy exposes `/{id}/api` and `/{id}/download`. It does not expose the alternate routes. Prowlarr uses indexer ID `0` for application connectivity tests.

For a Usenet download, Prowlarr records the grab and returns an HTTP 301 redirect to the indexer. The proxy follows the redirect and caches the NZB bytes. Caching the redirect alone would still require the client to download the NZB from the indexer.

Prowlarr's [Newznab results](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/IndexerSearch/NewznabResults.cs) include a release GUID and download URLs in the RSS item's `link` and `enclosure` fields. The proxy rewrites both download URLs and preserves the other fields.

## Cache identity

Prowlarr's [download mapping service](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Download/DownloadMappingService.cs) wraps the indexer URL in a Prowlarr download link. Its [protection service](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Security/ProtectionService.cs) uses a new initialization vector each time it encrypts a URL. The wrapped link can therefore change between searches for the same release.

For search results with a GUID, the proxy signs a cache identity based on the Prowlarr instance, indexer, API key, and GUID. It binds that identity to the download request. A cache hit can then bypass Prowlarr and the indexer.

This depends on the indexer providing a stable, unique GUID. Prowlarr's [RSS parser](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Indexers/RssParser.cs) generates a random GUID when one is missing. An unstable GUID can cause cache misses.

For unsigned links, the proxy first checks the wrapped URL. On a miss, it resolves the Prowlarr redirect and checks the destination URL. Resolving a redirect records a grab in Prowlarr and can count toward its grab limit. The destination URL can also change when an indexer changes its tokens.

## Application sync

Prowlarr's [Radarr](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Radarr/Radarr.cs) and [Sonarr](https://github.com/Prowlarr/Prowlarr/blob/v2.4.0.5391/src/NzbDrone.Core/Applications/Sonarr/Sonarr.cs) integrations use the **Prowlarr Server** URL to build synchronized indexer URLs. Set that URL to the proxy address so future syncs retain the proxy route.

The application test uses indexer ID `0`. Prowlarr sends a synthetic indexer to the application's test endpoint, and the application tests it through the configured proxy URL. The proxy does not need to expose Prowlarr's administration API.

The [integration test guide](../tests/integration/README.md) describes the covered versions and test limits.
