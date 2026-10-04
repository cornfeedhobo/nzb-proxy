package proxy

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cornfeedhobo/nzb-proxy/internal/cache"
)

const nzb = `<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="test"><segments><segment bytes="10" number="1">message@example</segment></segments></file></nzb>`

func setup(t *testing.T, upstream http.HandlerFunc) (*gateway, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(upstream)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL + "/prowlarr")
	p, _ := url.Parse("http://proxy.test/base")
	store, err := cache.New(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{UpstreamURL: u, PublicURL: p, APIKey: "secret", SigningKey: []byte(strings.Repeat("s", 32)), FetchTimeout: time.Second}, store)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*gateway), s
}
func serve(g http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestConcurrentDownloadsAndAuthorization(t *testing.T) {
	var hits atomic.Int32
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, nzb)
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			w := serve(g, "http://proxy.test/base/1/download?apikey=secret&link=one&file=x")
			if w.Code != 200 || w.Body.String() != nzb {
				t.Errorf("download status %d", w.Code)
			}
		})
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("upstream requests = %d", hits.Load())
	}
	if w := serve(g, "http://proxy.test/base/1/download?link=one&file=x"); w.Code != 401 {
		t.Fatalf("unauthorized cache access: %d", w.Code)
	}
	r := httptest.NewRequest("HEAD", "http://proxy.test/base/1/download?apikey=secret", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 405 || hits.Load() != 1 {
		t.Fatal("HEAD fetched download")
	}
}

func TestSignedGUIDAcrossRandomizedLinks(t *testing.T) {
	var hits atomic.Int32
	var searches atomic.Int32
	var upstreamURL string
	g, s := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api") {
			w.Header().Set("Content-Type", "application/rss+xml")
			n := searches.Add(1)
			link := fmt.Sprintf("%s/prowlarr/1/download?apikey=secret&link=random-%d&file=title", upstreamURL, n)
			fmt.Fprintf(w, `<rss><channel><item><guid>stable-release</guid><link>%s</link><enclosure url="%s"/></item></channel></rss>`, xmlEscape(link), xmlEscape(link))
			return
		}
		hits.Add(1)
		fmt.Fprint(w, nzb)
	})
	upstreamURL = s.URL
	for range 2 {
		w := serve(g, "http://proxy.test/base/1/api?apikey=secret&t=search")
		var feed struct {
			Channel struct {
				Item struct {
					Link      string `xml:"link"`
					Enclosure struct {
						URL string `xml:"url,attr"`
					} `xml:"enclosure"`
				} `xml:"item"`
			} `xml:"channel"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
			t.Fatal(err)
		}
		link := feed.Channel.Item.Link
		if !strings.HasPrefix(link, "http://proxy.test/base/1/download?") || !strings.Contains(link, "nzbproxy_sig=") {
			t.Fatalf("unrewritten link %s", link)
		}
		if link != feed.Channel.Item.Enclosure.URL {
			t.Fatal("enclosure differs")
		}
		if w := serve(g, link); w.Code != 200 {
			t.Fatalf("signed download status %d", w.Code)
		}
		u, _ := url.Parse(link)
		q := u.Query()
		q.Set("link", "tampered")
		u.RawQuery = q.Encode()
		if w := serve(g, u.String()); w.Code != 400 {
			t.Fatal("accepted tampered link")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("randomized links fetched %d times", hits.Load())
	}
}

func TestDisconnectedWaiterDoesNotCancelFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	g, s := setup(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-release; fmt.Fprint(w, nzb) })
	u, _ := url.Parse(s.URL + "/prowlarr/1/download")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := g.download(ctx, "key", u); done <- err }()
	<-started
	cancel()
	if <-done == nil {
		t.Fatal("wanted cancellation")
	}
	close(release)
	e, err := g.download(context.Background(), "key", u)
	if err != nil || string(e.Data) != nzb {
		t.Fatalf("shared fetch lost: %v", err)
	}
}

func TestInvalidAndOversizedResponsesNotCached(t *testing.T) {
	for _, body := range []string{"<html>error</html>", "<nzb></nzb>", "<nzb><segment>x</segment>", nzb} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			var hits atomic.Int32
			g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, body) })
			if body == nzb {
				g.cfg.MaxNZBBytes = 10
			}
			for range 2 {
				if w := serve(g, "http://proxy.test/base/1/download?apikey=secret&link=x"); w.Code != 502 {
					t.Fatalf("accepted bad payload: %d", w.Code)
				}
			}
			if hits.Load() != 2 {
				t.Fatal("cached invalid payload")
			}
		})
	}
}

func TestRedirectPolicy(t *testing.T) {
	var downstreamHits atomic.Int32
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downstreamHits.Add(1); fmt.Fprint(w, nzb) }))
	defer indexer.Close()
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, indexer.URL, http.StatusFound) })
	w := serve(g, "http://proxy.test/base/1/download?apikey=secret&link=x")
	if w.Code != 502 || downstreamHits.Load() != 0 {
		t.Fatal("followed unapproved redirect")
	}
	req, _ := http.NewRequest("GET", "https://indexer.test/get?apikey=secret", nil)
	req.Header.Set("X-Api-Key", "secret")
	req.Header.Set("Cookie", "session=secret")
	g.cfg.AllowedHosts = []string{"indexer.test"}
	if err := g.client.CheckRedirect(req, nil); err != nil {
		t.Fatal(err)
	}
	if req.URL.Query().Get("apikey") != "" || req.Header.Get("X-Api-Key") != "" || req.Header.Get("Cookie") != "" {
		t.Fatal("forwarded Prowlarr credentials")
	}
}

func TestUpstreamRedirectAndPersistence(t *testing.T) {
	var hits atomic.Int32
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			http.Redirect(w, r, "/payload", 302)
			return
		}
		hits.Add(1)
		io.WriteString(w, nzb)
	})
	if w := serve(g, "http://proxy.test/base/1/download?apikey=secret&link=x"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	h, err := New(g.cfg, g.store)
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(h, "http://proxy.test/base/1/download?apikey=secret&link=x"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if hits.Load() != 1 {
		t.Fatal("cache not reused by new gateway")
	}
}

func TestUnsignedDifferentWrappersResolveSameNZB(t *testing.T) {
	var resolutions, downloads atomic.Int32
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			resolutions.Add(1)
			http.Redirect(w, r, "/payload?id=one&token=account", 302)
			return
		}
		downloads.Add(1)
		fmt.Fprint(w, nzb)
	})
	for _, link := range []string{"random-a", "random-b", "random-b"} {
		if w := serve(g, "http://proxy.test/base/1/download?apikey=secret&link="+link); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if resolutions.Load() != 2 || downloads.Load() != 1 {
		t.Fatalf("resolutions=%d downloads=%d", resolutions.Load(), downloads.Load())
	}
}

func TestRewriteRejectsExternalDownloadLinks(t *testing.T) {
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<rss><channel><item><guid>one</guid><link>https://indexer.example/nzb?id=one</link></item></channel></rss>`)
	})
	if w := serve(g, "http://proxy.test/base/1/api?apikey=secret&t=search"); w.Code != 502 {
		t.Fatalf("allowed bypass: %d", w.Code)
	}
}

func TestSignedIndexerIsolation(t *testing.T) {
	var hits atomic.Int32
	g, s := setup(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, nzb) })
	for _, id := range []string{"1", "2"} {
		link := g.signedLink(s.URL+"/prowlarr/"+id+"/download?apikey=secret&link=random", "same-guid")
		if w := serve(g, link); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if hits.Load() != 2 {
		t.Fatal("merged distinct indexers")
	}
}

func TestSyntheticIndexerZero(t *testing.T) {
	search := `<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><item><title>Test Release</title><guid>https://prowlarr.com</guid><link>https://prowlarr.com</link><enclosure url="https://prowlarr.com" type="application/x-nzb"/></item></channel></rss>`
	caps := `<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`
	g, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, caps)
		} else {
			fmt.Fprint(w, search)
		}
	})
	for query, expected := range map[string]string{"search": search, "caps": caps} {
		w := serve(g, "http://proxy.test/base/0/api?apikey=secret&t="+query)
		if w.Code != 200 || w.Body.String() != expected {
			t.Fatalf("synthetic %s response: status %d", query, w.Code)
		}
	}
}
