"""Controlled Newznab provider; never contacts an indexer or Usenet server."""
import json
import ssl
import threading
import time
from collections import Counter
from email.utils import formatdate
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit
from xml.sax.saxutils import escape

NZB = b'''<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
<file poster="integration@example.invalid" date="1700000000" subject="fixture.bin">
<groups><group>alt.binaries.test</group></groups>
<segments><segment bytes="123" number="1">fixture@example.invalid</segment></segments>
</file></nzb>'''
COUNTS = Counter()
LOCK = threading.Lock()
CAPS = b'''<?xml version="1.0"?>
<caps><server version="1.0" title="Integration fixture"/>
<limits max="100" default="100"/><registration available="no" open="no"/>
<searching><search available="yes" supportedParams="q"/>
<tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
<movie-search available="yes" supportedParams="q,imdbid,tmdbid"/></searching>
<categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/>
</category><category id="5000" name="TV"><subcat id="5040" name="TV/HD"/>
</category></categories></caps>'''


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Avoid recording even test credential-bearing URLs.

    def do_GET(self):
        query = parse_qs(urlsplit(self.path).query)
        operation = query.get("t", [""])[0]
        if self.path == "/stats":
            with LOCK:
                data = json.dumps(COUNTS).encode()
            return self.reply(data, "application/json")
        if operation == "caps":
            return self.reply(CAPS)
        if operation == "get":
            release = query.get("id", ["good"])[0]
            with LOCK:
                COUNTS[release] += 1
            time.sleep(0.3)  # Force concurrent cache misses to overlap.
            return self.reply(b"<html>provider error</html>" if release == "bad" else NZB,
                              "application/x-nzb")
        if operation in ("search", "movie", "tvsearch"):
            release = "bad" if "bad" in query.get("q", [""])[0] else "good"
            link = escape(f"https://fixture/api?t=get&id={release}&apikey=fixture-key")
            data = f'''<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel><title>Integration fixture</title><description>Local only</description>
<link>http://fixture:8080/</link><newznab:response offset="0" total="1"/>
<item><title>Integration.Movie.2024.1080p.WEB-DL.H264-TEST</title>
<guid isPermaLink="false">urn:nzb-proxy:fixture:{release}</guid><link>{link}</link>
<pubDate>{formatdate(usegmt=True)}</pubDate><size>1234567890</size>
<category>Movies &gt; HD</category>
<enclosure url="{link}" length="1234567890" type="application/x-nzb"/>
<newznab:attr name="category" value="2040"/>
<newznab:attr name="size" value="1234567890"/>
</item></channel></rss>'''
            return self.reply(data.encode())
        self.send_error(404)

    def reply(self, data, content_type="application/xml"):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


if __name__ == "__main__":
    secure = ThreadingHTTPServer(("0.0.0.0", 443), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain("/tls/cert.pem", "/tls/key.pem")
    secure.socket = context.wrap_socket(secure.socket, server_side=True)
    threading.Thread(target=secure.serve_forever, daemon=True).start()
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
