//go:build e2e

// The fixture serves Newznab search results and counts downloads of a synthetic NZB.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const fixtureNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
<file poster="integration@example.invalid" date="1700000000" subject="fixture.bin">
<groups><group>alt.binaries.test</group></groups>
<segments><segment bytes="123" number="1">fixture@example.invalid</segment></segments>
</file></nzb>`

const caps = `<?xml version="1.0"?>
<caps><server version="1.0" title="Integration fixture"/>
<limits max="100" default="100"/><registration available="no" open="no"/>
<searching><search available="yes" supportedParams="q"/>
<tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
<movie-search available="yes" supportedParams="q,imdbid,tmdbid"/></searching>
<categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/>
</category><category id="5000" name="TV"><subcat id="5040" name="TV/HD"/>
</category></categories></caps>`

const results = `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel><title>Integration fixture</title><description>Local only</description>
<link>http://fixture:8080/</link><newznab:response offset="0" total="1"/>
<item><title>Integration.Movie.2024.1080p.WEB-DL.H264-TEST</title>
<guid isPermaLink="false">urn:nzb-proxy:fixture:%[1]s</guid><link>%[2]s</link>
<pubDate>%[3]s</pubDate><size>1234567890</size><category>Movies &gt; HD</category>
<enclosure url="%[2]s" length="1234567890" type="application/x-nzb"/>
<newznab:attr name="category" value="2040"/><newznab:attr name="size" value="1234567890"/>
</item></channel></rss>`

func main() {
	var mu sync.Mutex
	counts := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(counts)
	})
	download := func(w http.ResponseWriter, id string) {
		mu.Lock()
		counts[id]++
		mu.Unlock()
		time.Sleep(300 * time.Millisecond) // Make concurrent cache misses overlap.
		w.Header().Set("Content-Type", "application/x-nzb")
		if id == "bad" {
			fmt.Fprint(w, "<html>provider error</html>")
			return
		}
		fmt.Fprint(w, fixtureNZB)
	}
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		switch q.Get("t") {
		case "caps":
			fmt.Fprint(w, caps)
		case "get":
			download(w, q.Get("id"))
		case "search", "movie", "tvsearch":
			id := "good"
			if strings.Contains(q.Get("q"), "bad") {
				id = "bad"
			}
			link := "https://fixture/api?t=get&amp;id=" + id + "&amp;apikey=fixture-key"
			fmt.Fprintf(w, results, id, link, time.Now().UTC().Format(http.TimeFormat))
		default:
			http.NotFound(w, r)
		}
	})
	go func() { log.Fatal(http.ListenAndServeTLS(":443", "/tls/cert.pem", "/tls/key.pem", mux)) }()
	log.Fatal(http.ListenAndServe(":8080", mux))
}
