#!/usr/bin/env python3
"""Run real Prowlarr/Radarr against a private, counted Newznab provider."""
import concurrent.futures
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

from fixture import NZB

HERE = Path(__file__).resolve().parent
KEY = secrets.token_hex(16)
# Respect the workspace's command wrapper when installed; portable outside Codex.
PREFIX = ["rtk", "proxy"] if shutil.which("rtk") else []
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(url, data=None, method=None, status=200):
    req = urllib.request.Request(url, data=None if data is None else json.dumps(data).encode(),
                                 method=method, headers={"X-Api-Key": KEY,
                                                         "Content-Type": "application/json"})
    try:
        response = HTTP.open(req, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        if response.status != status:
            # Only local, randomly generated test credentials are ever in this response.
            raise AssertionError(f"{req.method} {urllib.parse.urlsplit(url).path}: "
                                 f"expected {status}, got {response.status}: {body[:2000]!r}")
        return body


def api(base, path, data=None, method=None, status=200):
    body = request(base + path, data, method, status)
    return json.loads(body) if body else None


def wait_for(check, description, seconds=120):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, AssertionError, ValueError) as error:
            last = error
        time.sleep(1)
    raise AssertionError(f"Timed out waiting for {description}: {last}")


def fields(resource, **values):
    existing = {field["name"]: field for field in resource["fields"]}
    for name, value in values.items():
        if name not in existing:
            raise AssertionError(f"Missing schema field: {name}")
        existing[name]["value"] = value
    return resource


def run():
    with tempfile.TemporaryDirectory(prefix="nzb-proxy-integration-") as directory:
        state = Path(directory)
        state.chmod(0o755)
        for service, port in (("prowlarr", 9696), ("radarr", 7878)):
            config = state / service
            config.mkdir(mode=0o777)
            config.chmod(0o777)
            (config / "config.xml").write_text(
                f"<Config><BindAddress>*</BindAddress><Port>{port}</Port>"
                f"<ApiKey>{KEY}</ApiKey><AuthenticationMethod>External</AuthenticationMethod>"
                "<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>"
                "<LaunchBrowser>False</LaunchBrowser><AnalyticsEnabled>False</AnalyticsEnabled>"
                "<LogLevel>info</LogLevel><UpdateAutomatically>False</UpdateAutomatically></Config>")
            (config / "config.xml").chmod(0o666)
        tls = state / "tls"
        tls.mkdir()
        subprocess.run(PREFIX + ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                                "-keyout", str(tls / "key.pem"), "-out", str(tls / "cert.pem"),
                                "-days", "2", "-subj", "/CN=fixture", "-addext",
                                "subjectAltName=DNS:fixture"], check=True, capture_output=True)
        env = dict(os.environ, TEST_STATE=str(state), TEST_API_KEY=KEY)
        command = PREFIX + ["docker", "compose", "-f", str(HERE / "compose.yaml"),
                            "-p", "nzb-proxy-test-" + secrets.token_hex(4)]

        def compose(*args, capture=False, check=True):
            return subprocess.run(command + list(args), env=env, check=check, text=True,
                                  stdout=subprocess.PIPE if capture else None)

        def endpoint(service, port):
            address = compose("port", service, str(port), capture=True).stdout.strip()
            assert address, f"Docker did not publish {service}:{port}"
            return "http://" + address

        try:
            compose("up", "-d", "--build")
            p = endpoint("prowlarr", 9696)
            r = endpoint("radarr", 7878)
            proxy = endpoint("proxy", 8080)
            fixture = endpoint("fixture", 8080)
            for base, path in ((p, "/api/v1/system/status"), (r, "/api/v3/system/status"),
                               (proxy, "/healthz"), (fixture, "/stats")):
                wait_for(lambda: request(base + path), path)
            print("PASS: real Prowlarr, Radarr, proxy and HTTPS fixture started", flush=True)

            schema = api(p, "/api/v1/indexer/schema")
            indexer = next(x for x in schema if x["implementation"] == "Newznab")
            indexer.update(name="Local fixture", enable=True, appProfileId=1,
                           priority=25, tags=[])
            fields(indexer, baseUrl="http://fixture:8080", apiPath="/api", apiKey="fixture-key")
            indexer = api(p, "/api/v1/indexer", indexer, status=201)
            indexer_id = indexer["id"]

            app = next(x for x in api(p, "/api/v1/applications/schema")
                       if x["implementation"] == "Radarr")
            app.update(name="Integration Radarr", syncLevel="fullSync", tags=[])
            fields(app, prowlarrUrl="http://proxy:8080/cache", baseUrl="http://radarr:7878",
                   apiKey=KEY, syncCategories=[2000, 2040])
            api(p, "/api/v1/applications/test", app, status=200)
            api(p, "/api/v1/applications", app, status=201)

            def synced():
                return next((x for x in api(r, "/api/v3/indexer")
                             if any(f["name"] == "baseUrl" and
                                    f.get("value") == f"http://proxy:8080/cache/{indexer_id}/"
                                    for f in x["fields"])), None)

            synced_indexer = wait_for(synced, "Radarr indexer sync")
            api(r, "/api/v3/indexer/test", synced_indexer)
            print("PASS: Prowlarr application test and sync; Radarr tests synced proxy URL", flush=True)

            def search(term):
                body = request(f"{proxy}/cache/{indexer_id}/api?t=search&q={term}&apikey={KEY}")
                item = ET.fromstring(body).find("./channel/item")
                assert item is not None, "Prowlarr returned no release"
                link = item.findtext("link")
                assert link == item.find("enclosure").attrib["url"], "Download fields differ"
                assert link.startswith(f"http://proxy:8080/cache/{indexer_id}/download?"), link
                query = urllib.parse.parse_qs(urllib.parse.urlsplit(link).query)
                assert query.get("nzbproxy_id") and query.get("nzbproxy_sig"), "Unsigned link"
                return proxy + link.removeprefix("http://proxy:8080"), query

            first, q1 = search("good-first")
            second, q2 = search("good-second")
            assert q1["link"] != q2["link"], "Expected different real Prowlarr encrypted wrappers"
            assert q1["nzbproxy_id"] == q2["nzbproxy_id"], "GUID identity changed"
            assert api(fixture, "/stats").get("good", 0) == 0, "Search unexpectedly grabbed NZB"
            with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
                bodies = list(pool.map(request, [first, second] * 8))
            assert all(body == NZB for body in bodies), "NZB bytes changed"
            assert api(fixture, "/stats")["good"] == 1, "Concurrent duplicate provider grabs"
            def grabs():
                records = api(p, "/api/v1/history?page=1&pageSize=100")["records"]
                return sum(x["eventType"] == "releaseGrabbed" for x in records)

            history = grabs()
            assert history == 1, f"Expected one Prowlarr grab, got {history}"
            third, _ = search("good-third")
            assert request(third) == NZB
            print("PASS: 16 downloads across random wrappers produce one provider fetch and grab", flush=True)

            compose("restart", "proxy")
            old_proxy = proxy
            proxy = endpoint("proxy", 8080)
            # Docker can reassign ephemeral host ports on restart. The signed
            # public URL inside the network remains http://proxy:8080/cache/...
            first = proxy + first.removeprefix(old_proxy)
            wait_for(lambda: request(proxy + "/healthz"), "proxy restart")
            assert request(first) == NZB, "Old signed link failed after restart"
            fourth, _ = search("good-fourth")
            assert request(fourth) == NZB
            assert api(fixture, "/stats")["good"] == 1
            assert grabs() == history
            print("PASS: cache and signing key survive restart; cache hits add no Prowlarr grabs", flush=True)

            # Explicitly wrong query credentials override the request helper's valid header.
            request(first.replace("apikey=" + KEY, "apikey=wrong"), status=401)
            request(first + "&file=tampered", status=400)
            bad, _ = search("bad")
            request(bad, status=502)
            request(bad, status=502)
            assert api(fixture, "/stats")["bad"] == 2, "Invalid response was cached"
            print("PASS: authentication, signature binding and invalid-NZB rejection", flush=True)
            # A cached signed request should work with both upstream services unavailable.
            compose("stop", "prowlarr", "fixture")
            assert request(first) == NZB
            print("PASS: saved NZB available while Prowlarr and provider are offline", flush=True)
        except BaseException:
            compose("logs", "--no-color", "--tail", "100", check=False)
            raise
        finally:
            compose("down", "--volumes", "--remove-orphans")


if __name__ == "__main__":
    run()
