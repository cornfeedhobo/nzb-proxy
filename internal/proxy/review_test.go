package proxy

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cornfeedhobo/nzb-proxy/internal/cache"
)

func reviewGateway(t *testing.T, upstream, public string) *gateway {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	p, err := url.Parse(public)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cache.New(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{UpstreamURL: u, PublicURL: p, APIKey: "review-secret", SigningKey: []byte(strings.Repeat("s", 32)), AllowedHosts: []string{"indexer.example"}, FetchTimeout: time.Second}, s)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*gateway)
}

func TestReviewRejectMalformedDocuments(t *testing.T) {
	valid := `<nzb><file><segments><segment bytes="10" number="1">message@example</segment></segments></file></nzb>`
	if !validNZB([]byte(valid)) {
		t.Fatal("valid NZB rejected")
	}
	for _, doc := range []string{strings.TrimSuffix(valid, "</nzb>"), valid + valid, valid + "garbage", "garbage" + valid, `<nzb><file><segments><segment> </segment></segments></file></nzb>`, `<nzb><file><segments><segment>x</segments></file></nzb>`, `<nzb><segment>x</segment></nzb>`} {
		if validNZB([]byte(doc)) {
			t.Errorf("accepted malformed document: %s", doc)
		}
	}
}

func TestReviewRewriteXMLVariants(t *testing.T) {
	g := reviewGateway(t, "http://prowlarr:9696", "http://proxy.example")
	raw := "http://prowlarr:9696/1/download?apikey=review-secret&link=abc"
	for _, tc := range []struct{ name, link, enclosure string }{
		{"escaped", strings.ReplaceAll(raw, "&", "&amp;"), strings.ReplaceAll(raw, "&", "&amp;")},
		{"numeric-entity", strings.ReplaceAll(raw, "&", "&#38;"), strings.ReplaceAll(raw, "&", "&#38;")},
		{"cdata", "<![CDATA[" + raw + "]]>", strings.ReplaceAll(raw, "&", "&amp;")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><item><guid>stable-guid</guid><link>` + tc.link + `</link><enclosure url="` + tc.enclosure + `"/><newznab:attr name="category" value="2000"/></item></channel></rss>`
			output := g.rewrite([]byte(input))
			var feed struct {
				Channel struct {
					Item struct {
						Link      string `xml:"link"`
						Enclosure struct {
							URL string `xml:"url,attr"`
						} `xml:"enclosure"`
						Attr struct {
							Name  xml.Name
							Key   string `xml:"name,attr"`
							Value string `xml:"value,attr"`
						} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
					} `xml:"item"`
				} `xml:"channel"`
			}
			if err := xml.Unmarshal(output, &feed); err != nil {
				t.Fatal(err)
			}
			item := feed.Channel.Item
			for _, link := range []string{item.Link, item.Enclosure.URL} {
				u, err := url.Parse(link)
				if err != nil || u.Host != "proxy.example" || u.Query().Get("nzbproxy_id") == "" || u.Query().Get("nzbproxy_sig") == "" {
					t.Errorf("download link not signed and rewritten")
				}
			}
			if item.Attr.Key != "category" || item.Attr.Value != "2000" {
				t.Error("Newznab namespace attributes lost")
			}
		})
	}
}

func TestReviewPublicBasePathBoundary(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(`<rss/>`)) }))
	defer upstream.Close()
	g := reviewGateway(t, upstream.URL, "http://proxy.example/proxy")
	for _, path := range []string{"/1/api", "/proxy1/api", "/proxy-extra/1/api"} {
		r := httptest.NewRequest("GET", path+"?apikey=review-secret", nil)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("out-of-base requests reached upstream %d times", calls.Load())
	}
	good := httptest.NewRecorder()
	g.ServeHTTP(good, httptest.NewRequest("GET", "/proxy/1/api?apikey=review-secret", nil))
	if good.Code != 200 || calls.Load() != 1 {
		t.Fatalf("valid base path failed: status=%d calls=%d", good.Code, calls.Load())
	}
}

func TestReviewRedirectRemovesCredentials(t *testing.T) {
	g := reviewGateway(t, "http://prowlarr:9696", "http://proxy.example")
	initial, _ := http.NewRequest("GET", "http://prowlarr:9696/1/download?apikey=review-secret", nil)
	next, _ := http.NewRequest("GET", "https://indexer.example/get?apikey=review-secret", nil)
	for _, name := range []string{"X-Api-Key", "Authorization", "Cookie", "Referer"} {
		next.Header.Set(name, "review-secret")
	}
	if err := g.client.CheckRedirect(next, []*http.Request{initial}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"X-Api-Key", "Authorization", "Cookie", "Referer"} {
		if next.Header.Get(name) != "" {
			t.Errorf("redirect leaked %s", name)
		}
	}
	if next.URL.Query().Get("apikey") != "" {
		t.Error("redirect leaked API key in query")
	}
}

func TestReviewCanceledWaiterDoesNotCancelSharedDownload(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		w.Write([]byte(`<nzb><file><segments><segment bytes="10" number="1">message@example</segment></segments></file></nzb>`))
	}))
	defer upstream.Close()
	g := reviewGateway(t, upstream.URL, "http://proxy.example")
	u, _ := url.Parse(upstream.URL + "/1/download")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := g.download(ctx, "same-key", u); done <- err }()
	<-started
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("canceled caller got %v", err)
	}
	close(release)
	entry, err := g.download(context.Background(), "same-key", u)
	if err != nil || len(entry.Data) == 0 {
		t.Fatalf("other waiter failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate upstream fetches: %d", calls.Load())
	}
}
