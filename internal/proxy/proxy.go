// Package proxy provides the authenticated Newznab gateway and NZB cache.
package proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cornfeedhobo/nzb-proxy/internal/cache"
)

type Config struct {
	UpstreamURL  *url.URL
	PublicURL    *url.URL
	APIKey       string
	SigningKey   []byte
	AllowedHosts []string
	FetchTimeout time.Duration
	MaxNZBBytes  int64
}

type flight struct {
	done  chan struct{}
	entry cache.Entry
	err   error
}
type gateway struct {
	cfg     Config
	store   *cache.Store
	client  *http.Client
	mu      sync.Mutex
	flights map[string]*flight
}

func New(cfg Config, store *cache.Store) (http.Handler, error) {
	if cfg.UpstreamURL == nil || cfg.PublicURL == nil || cfg.UpstreamURL.Host == "" || cfg.PublicURL.Host == "" || cfg.APIKey == "" || store == nil {
		return nil, errors.New("upstream, public URL, API key and cache are required")
	}
	for _, u := range []*url.URL{cfg.UpstreamURL, cfg.PublicURL} {
		if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid gateway URL")
		}
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 2 * time.Minute
	}
	if cfg.MaxNZBBytes <= 0 {
		cfg.MaxNZBBytes = 32 << 20
	}
	if len(cfg.SigningKey) < 32 {
		return nil, errors.New("signing key must contain at least 32 bytes")
	}
	g := &gateway{cfg: cfg, store: store, flights: make(map[string]*flight)}
	g.client = &http.Client{Timeout: cfg.FetchTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		if len(via) >= 10 || !g.allowed(req.URL) {
			return errors.New("redirect destination is not allowed")
		}
		if req.URL.Host != cfg.UpstreamURL.Host {
			req.Header.Del("X-Api-Key")
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			if req.URL.Query().Get("apikey") == cfg.APIKey {
				q := req.URL.Query()
				q.Del("apikey")
				req.URL.RawQuery = q.Encode()
			}
		}
		return nil
	}}
	return g, nil
}

func (g *gateway) allowed(u *url.URL) bool {
	if u.User != nil {
		return false
	}
	if u.Scheme == g.cfg.UpstreamURL.Scheme && u.Host == g.cfg.UpstreamURL.Host {
		return true
	}
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	for _, host := range g.cfg.AllowedHosts {
		if strings.EqualFold(host, u.Hostname()) {
			return true
		}
	}
	return false
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("apikey")
	if key == "" {
		key = r.Header.Get("X-Api-Key")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(g.cfg.APIKey)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base := strings.TrimRight(g.cfg.PublicURL.Path, "/")
	if !strings.HasPrefix(r.URL.Path, base+"/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, base)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || (parts[1] != "api" && parts[1] != "download") {
		http.NotFound(w, r)
		return
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			http.NotFound(w, r)
			return
		}
	}
	u := *g.cfg.UpstreamURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.Join(parts, "/")
	u.RawPath = ""
	q := r.URL.Query()
	q.Set("apikey", g.cfg.APIKey)
	u.RawQuery = q.Encode()
	if parts[1] == "download" || strings.EqualFold(q.Get("t"), "get") {
		identity := u
		iq := identity.Query()
		signedID := iq.Get("nzbproxy_id")
		sig := iq.Get("nzbproxy_sig")
		iq.Del("nzbproxy_sig")
		identity.RawQuery = iq.Encode()
		if signedID != "" && !hmac.Equal([]byte(sig), []byte(g.sign(&identity))) {
			http.Error(w, "invalid cache identity", 400)
			return
		}
		iq.Del("nzbproxy_id")
		iq.Del("apikey")
		identity.RawQuery = iq.Encode()
		material := identity.String()
		if signedID != "" {
			material = g.cfg.UpstreamURL.String() + "/" + parts[0] + "/guid/" + signedID
		}
		sum := sha256.Sum256([]byte(g.cfg.APIKey + "\x00" + material))
		cacheKey := hex.EncodeToString(sum[:])
		q.Del("nzbproxy_id")
		q.Del("nzbproxy_sig")
		u.RawQuery = q.Encode()
		e, err := g.download(r.Context(), cacheKey, &u)
		if err != nil {
			http.Error(w, "NZB retrieval failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Header().Set("Cache-Control", "private, no-store")
		if e.Filename != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.Filename}))
		}
		w.Write(e.Data)
		return
	}
	g.search(w, r, &u)
}

func (g *gateway) download(ctx context.Context, key string, u *url.URL) (cache.Entry, error) {
	return g.shared(ctx, key, func(ctx context.Context) (cache.Entry, error) { return g.fetch(ctx, u) })
}
func (g *gateway) shared(ctx context.Context, key string, fetch func(context.Context) (cache.Entry, error)) (cache.Entry, error) {
	g.mu.Lock()
	f := g.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		g.flights[key] = f
		go func() {
			defer func() { g.mu.Lock(); delete(g.flights, key); close(f.done); g.mu.Unlock() }()
			var ok bool
			f.entry, ok, f.err = g.store.Get(key)
			if f.err != nil || ok {
				return
			}
			fetchCtx, cancel := context.WithTimeout(context.Background(), g.cfg.FetchTimeout)
			defer cancel()
			f.entry, f.err = fetch(fetchCtx)
			if f.err == nil {
				f.err = g.store.Put(key, f.entry)
				if f.err == nil {
					var found bool
					f.entry, found, f.err = g.store.Get(key)
					if f.err == nil && !found {
						f.err = errors.New("cache entry expired during publication")
					}
				}
			}
		}()
	}
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return cache.Entry{}, ctx.Err()
	case <-f.done:
		return f.entry, f.err
	}
}

func (g *gateway) request(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nzb-proxy")
	return g.client.Do(req)
}
func (g *gateway) fetch(ctx context.Context, u *url.URL) (cache.Entry, error) {
	client := *g.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return cache.Entry{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return cache.Entry{}, errors.New("upstream request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
		target, err := resp.Location()
		if err != nil {
			return cache.Entry{}, errors.New("invalid redirect")
		}
		redirect, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return cache.Entry{}, errors.New("invalid redirect")
		}
		if err = g.client.CheckRedirect(redirect, []*http.Request{req}); err != nil {
			return cache.Entry{}, err
		}
		canonical := *redirect.URL
		canonical.RawQuery = canonical.Query().Encode()
		sum := sha256.Sum256([]byte(g.cfg.APIKey + "\x00" + g.cfg.UpstreamURL.String() + "\x00" + u.Path + "\x00" + canonical.String()))
		key := "resolved-" + hex.EncodeToString(sum[:])
		return g.shared(ctx, key, func(ctx context.Context) (cache.Entry, error) { return g.fetchDirect(ctx, redirect.URL) })
	}
	return g.readNZB(resp)
}
func (g *gateway) fetchDirect(ctx context.Context, u *url.URL) (cache.Entry, error) {
	resp, err := g.request(ctx, u)
	if err != nil {
		return cache.Entry{}, errors.New("upstream request failed")
	}
	defer resp.Body.Close()
	return g.readNZB(resp)
}
func (g *gateway) readNZB(resp *http.Response) (cache.Entry, error) {
	if resp.StatusCode != http.StatusOK {
		return cache.Entry{}, errors.New("upstream rejected download")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, g.cfg.MaxNZBBytes+1))
	if err != nil || int64(len(data)) > g.cfg.MaxNZBBytes {
		return cache.Entry{}, errors.New("invalid download size")
	}
	if !validNZB(data) {
		return cache.Entry{}, errors.New("invalid NZB document")
	}
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	return cache.Entry{Data: data, ContentType: "application/x-nzb", Filename: params["filename"]}, nil
}

func (g *gateway) sign(u *url.URL) string {
	h := hmac.New(sha256.New, g.cfg.SigningKey)
	h.Write([]byte(u.String()))
	return hex.EncodeToString(h.Sum(nil))
}

// signedLink binds a stable RSS GUID identity to this exact Prowlarr download
// request. Clients cannot select arbitrary cache entries or poison a GUID.
func (g *gateway) signedLink(raw, guid string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u = g.cfg.UpstreamURL.ResolveReference(u)
	if !sameOrigin(u, g.cfg.UpstreamURL) || u.User != nil {
		return ""
	}
	base := strings.TrimRight(g.cfg.UpstreamURL.Path, "/")
	if !strings.HasPrefix(u.Path, base+"/") || !strings.HasSuffix(u.Path, "/download") {
		return ""
	}
	u.Scheme = g.cfg.UpstreamURL.Scheme
	u.Host = g.cfg.UpstreamURL.Host
	q := u.Query()
	q.Del("nzbproxy_sig")
	q.Del("nzbproxy_id")
	if guid != "" {
		sum := sha256.Sum256([]byte(guid))
		q.Set("nzbproxy_id", hex.EncodeToString(sum[:]))
		q.Set("apikey", g.cfg.APIKey)
		u.RawQuery = q.Encode()
		q.Set("nzbproxy_sig", g.sign(u))
	}
	u.RawQuery = q.Encode()
	u.Scheme = g.cfg.PublicURL.Scheme
	u.Host = g.cfg.PublicURL.Host
	u.Path = strings.TrimRight(g.cfg.PublicURL.Path, "/") + strings.TrimPrefix(u.Path, strings.TrimRight(g.cfg.UpstreamURL.Path, "/"))
	return u.String()
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func xmlEscape(s string) []byte { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.Bytes() }

var linkElement = regexp.MustCompile(`(?s)(<link(?:\s[^>]*)?>).*?(</link\s*>)`)
var enclosureElement = regexp.MustCompile(`<enclosure\b[^>]*>`)
var urlAttribute = regexp.MustCompile(`\burl\s*=\s*(?:"[^"]*"|'[^']*')`)

// Use XML decoding to locate items and obtain decoded field values, then patch
// only URL fields. Retaining other bytes preserves namespace prefixes and attrs.
func (g *gateway) rewrite(data []byte) []byte {
	d := xml.NewDecoder(bytes.NewReader(data))
	var out bytes.Buffer
	last := int64(0)
	for {
		start := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return data
		}
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "item" {
			continue
		}
		var item struct {
			GUID      string `xml:"guid"`
			Link      string `xml:"link"`
			Enclosure struct {
				URL string `xml:"url,attr"`
			} `xml:"enclosure"`
		}
		if err := d.DecodeElement(&item, &element); err != nil {
			return data
		}
		end := d.InputOffset()
		raw := data[start:end]
		if (item.Link != "" && g.signedLink(item.Link, item.GUID) == "") || (item.Enclosure.URL != "" && g.signedLink(item.Enclosure.URL, item.GUID) == "") {
			return nil
		}
		if item.Link != "" {
			raw = linkElement.ReplaceAllFunc(raw, func(match []byte) []byte {
				parts := linkElement.FindSubmatch(match)
				return bytes.Join([][]byte{parts[1], xmlEscape(g.signedLink(item.Link, item.GUID)), parts[2]}, nil)
			})
		}
		if item.Enclosure.URL != "" {
			raw = enclosureElement.ReplaceAllFunc(raw, func(match []byte) []byte {
				return urlAttribute.ReplaceAllFunc(match, func([]byte) []byte {
					return append(append([]byte(`url="`), xmlEscape(g.signedLink(item.Enclosure.URL, item.GUID))...), '"')
				})
			})
		}
		out.Write(data[last:start])
		out.Write(raw)
		last = end
	}
	out.Write(data[last:])
	return out.Bytes()
}

func validNZB(data []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	var stack []string
	root := false
	segments := 0
	inSegment := false
	segmentText := ""
	for {
		t, err := d.Token()
		if err == io.EOF {
			return root && depth == 0 && segments > 0
		}
		if err != nil {
			return false
		}
		switch t := t.(type) {
		case xml.StartElement:
			if inSegment {
				return false
			}
			if depth == 0 {
				if root || t.Name.Local != "nzb" {
					return false
				}
				root = true
			}
			depth++
			if t.Name.Local == "segment" {
				if strings.Join(stack, "/") != "nzb/file/segments" {
					return false
				}
				inSegment = true
				segmentText = ""
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if t.Name.Local == "segment" {
				if strings.TrimSpace(segmentText) == "" {
					return false
				}
				segments++
				inSegment = false
			}
			depth--
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return false
			}
			if inSegment {
				segmentText += string(t)
			}
		}
	}
}

func (g *gateway) search(w http.ResponseWriter, r *http.Request, u *url.URL) {
	resp, err := g.request(r.Context(), u)
	if err != nil {
		http.Error(w, "upstream request failed", 502)
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil || len(data) > 32<<20 {
		http.Error(w, "invalid search response", 502)
		return
	}
	if !strings.HasSuffix(u.Path, "/0/api") {
		data = g.rewrite(data)
	}
	if data == nil {
		http.Error(w, "unsupported upstream download link", 502)
		return
	}
	// Replace only URL origins belonging to Prowlarr. XML escaping does not
	// affect these prefixes; JSON can optionally escape slashes.
	from := strings.TrimRight(g.cfg.UpstreamURL.String(), "/")
	to := strings.TrimRight(g.cfg.PublicURL.String(), "/")
	data = bytes.ReplaceAll(data, []byte(from+"/"), []byte(to+"/"))
	jfrom, _ := json.Marshal(from + "/")
	jto, _ := json.Marshal(to + "/")
	data = bytes.ReplaceAll(data, jfrom[1:len(jfrom)-1], jto[1:len(jto)-1])
	data = bytes.ReplaceAll(data, []byte(strings.ReplaceAll(from+"/", "/", `\/`)), []byte(strings.ReplaceAll(to+"/", "/", `\/`)))
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}
