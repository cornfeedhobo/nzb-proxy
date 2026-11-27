//go:build e2e

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{}}

func (s stack) request(endpoint string, payload any, status int) ([]byte, error) {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		method = http.MethodPost
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", s.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != status {
		return nil, fmt.Errorf("%s %s: got %d, want %d: %.2000s", method, req.URL.Path, resp.StatusCode, status, data)
	}
	return data, nil
}

func (s stack) mustRequest(t *testing.T, endpoint string, payload any, status int) []byte {
	t.Helper()
	data, err := s.request(endpoint, payload, status)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func waitFor(t *testing.T, description string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %v", description, err)
		}
		time.Sleep(time.Second)
	}
}

func (s stack) waitReady(t *testing.T, endpoint string) {
	t.Helper()
	waitFor(t, endpoint, func() error {
		_, err := s.request(endpoint, nil, 200)
		return err
	})
}

// Preserve the live application's defaults when posting a schema back to Prowlarr.
type resource map[string]any

func (r resource) field(name string) map[string]any {
	fields, _ := r["fields"].([]any)
	for _, raw := range fields {
		f, _ := raw.(map[string]any)
		if f["name"] == name {
			return f
		}
	}
	return nil
}

func (s stack) configure(t *testing.T, schema, implementation string, attrs resource, fields map[string]any) resource {
	t.Helper()
	for _, r := range decodeJSON[[]resource](t, s.mustRequest(t, schema, nil, 200)) {
		if r["implementation"] != implementation {
			continue
		}
		maps.Copy(r, attrs)
		for name, value := range fields {
			field := r.field(name)
			if field == nil {
				t.Fatalf("%s schema is missing %s", implementation, name)
			}
			field["value"] = value
		}
		return r
	}
	t.Fatalf("missing %s schema", implementation)
	return nil
}

func (s stack) syncRadarr(t *testing.T, prowlarr, radarr string) int {
	t.Helper()
	indexers := prowlarr + "/api/v1/indexer"
	indexer := s.configure(t, indexers+"/schema", "Newznab",
		resource{"name": "Local fixture", "enable": true, "appProfileId": 1, "priority": 25, "tags": []int{}},
		map[string]any{"baseUrl": "http://fixture:8080", "apiPath": "/api", "apiKey": "fixture-key"})
	created := decodeJSON[struct{ ID int }](t, s.mustRequest(t, indexers, indexer, 201))
	apps := prowlarr + "/api/v1/applications"
	app := s.configure(t, apps+"/schema", "Radarr",
		resource{"name": "Integration Radarr", "syncLevel": "fullSync", "tags": []int{}},
		map[string]any{"prowlarrUrl": "http://nzb-proxy:8080/cache", "baseUrl": "http://radarr:7878",
			"apiKey": s.key, "syncCategories": []int{2000, 2040}})
	s.mustRequest(t, apps+"/test", app, 200)
	s.mustRequest(t, apps, app, 201)

	want := fmt.Sprintf("http://nzb-proxy:8080/cache/%d/", created.ID)
	var synced resource
	waitFor(t, "Radarr indexer sync", func() error {
		data, err := s.request(radarr+"/api/v3/indexer", nil, 200)
		if err != nil {
			return err
		}
		for _, r := range decodeJSON[[]resource](t, data) {
			if r.field("baseUrl")["value"] == want {
				synced = r
				return nil
			}
		}
		return fmt.Errorf("no indexer with base URL %s", want)
	})
	s.mustRequest(t, radarr+"/api/v3/indexer/test", synced, 200)
	return created.ID
}

func (s stack) search(t *testing.T, proxy string, id int, term string) *url.URL {
	t.Helper()
	base := fmt.Sprintf("/cache/%d/", id)
	body := s.mustRequest(t, proxy+base+"api?t=search&q="+term+"&apikey="+s.key, nil, 200)
	var rss struct {
		Items []struct {
			Link      string `xml:"link"`
			Enclosure struct {
				URL string `xml:"url,attr"`
			} `xml:"enclosure"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil {
		t.Fatal(err)
	}
	if len(rss.Items) == 0 {
		t.Fatal("search returned no release")
	}
	item := rss.Items[0]
	if item.Link != item.Enclosure.URL {
		t.Fatal("link and enclosure URLs differ")
	}
	link, err := url.Parse(item.Link)
	if err != nil || link.Scheme != "http" || link.Host != "nzb-proxy:8080" || link.Path != base+"download" {
		t.Fatal("download link does not use the public proxy route")
	}
	for _, field := range []string{"link", "nzbproxy_id", "nzbproxy_sig"} {
		if link.Query().Get(field) == "" {
			t.Fatalf("download link missing %s", field)
		}
	}
	return link
}

func TestEndToEnd(t *testing.T) {
	s := startStack(t)
	prowlarr := forward(t, "prowlarr", "9696")
	radarr := forward(t, "radarr", "7878")
	fixture := forward(t, "fixture", "8080")
	s.waitReady(t, prowlarr+"/api/v1/system/status")
	s.waitReady(t, radarr+"/api/v3/system/status")
	s.waitReady(t, fixture+"/stats")

	const chart = "../../deploy/helm"
	t.Log("Testing the Kustomize deployment with Prowlarr and Radarr")
	run(t, "kubectl", "apply", "-k", ".")
	prepareProxy(t)
	id := s.syncRadarr(t, prowlarr, radarr)
	s.checkCache(t, prowlarr, fixture, id, "app=nzb-proxy", func() {
		run(t, "kubectl", "rollout", "restart", "deployment/nzb-proxy")
	})
	run(t, "kubectl", "delete", "-k", ".", "--wait", "--timeout=2m")

	t.Log("Testing the Helm release with the same applications and a fresh cache")
	run(t, "helm", "lint", "--strict", chart)
	run(t, "helm", "install", "nzb-proxy", chart, "--wait", "--timeout", "2m",
		"--set", "image.repository=nzb-proxy,image.tag=e2e,image.pullPolicy=Never",
		"--set", "config.publicURL=http://nzb-proxy:8080/cache,config.allowedHosts={fixture}")
	prepareProxy(t)
	cached := s.checkCache(t, prowlarr, fixture, id, "app.kubernetes.io/instance=nzb-proxy", func() {
		run(t, "helm", "upgrade", "nzb-proxy", chart, "--reuse-values", "--set", "config.cacheTTL=72h", "--wait", "--timeout", "2m")
	})
	run(t, "kubectl", "delete", "pod", "prowlarr", "fixture", "--wait", "--timeout=2m")
	if got := string(s.mustRequest(t, cached, nil, 200)); got != fixtureNZB {
		t.Fatal("offline cache returned different NZB bytes")
	}
}

func prepareProxy(t *testing.T) {
	t.Helper()
	run(t, "kubectl", "patch", "deployment/nzb-proxy", "--patch-file", "fixture-ca-patch.yaml")
	waitForProxy(t)
	run(t, "kubectl", "exec", "deployment/nzb-proxy", "--", "/usr/local/bin/nzb-proxy", "--healthcheck")
}

func waitForProxy(t *testing.T) {
	t.Helper()
	run(t, "kubectl", "rollout", "status", "deployment/nzb-proxy", "--timeout=2m")
	// The Service endpoint can lag behind the Deployment's Ready condition.
	run(t, "kubectl", "exec", "fixture", "--", "timeout", "30", "sh", "-c", "until timeout -s KILL 3 wget -T 2 -qO /dev/null http://nzb-proxy:8080/healthz; do sleep 1; done")
}

func (s stack) checkCache(t *testing.T, prowlarr, fixture string, id int, selector string, rollout func()) string {
	t.Helper()
	proxy := forward(t, "nzb-proxy", "8080")
	count := func(release string) int {
		return decodeJSON[map[string]int](t, s.mustRequest(t, fixture+"/stats", nil, 200))[release]
	}
	grabs := func() int {
		history := decodeJSON[struct{ Records []struct{ EventType string } }](t,
			s.mustRequest(t, prowlarr+"/api/v1/history?page=1&pageSize=100", nil, 200))
		total := 0
		for _, record := range history.Records {
			if record.EventType == "releaseGrabbed" {
				total++
			}
		}
		return total
	}
	goodBefore, badBefore, grabsBefore := count("good"), count("bad"), grabs()
	first := s.search(t, proxy, id, "good-first")
	query := first.Query()
	signature := query.Get("nzbproxy_sig")
	query.Del("nzbproxy_sig")
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte("http://prowlarr:9696" + strings.TrimPrefix(first.Path, "/cache") + "?" + query.Encode()))
	if signature != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("link was not signed by the mounted Secret")
	}
	second := s.search(t, proxy, id, "good-second")
	if first.Query().Get("link") == second.Query().Get("link") {
		t.Fatal("expected different encrypted wrappers")
	}
	if first.Query().Get("nzbproxy_id") != second.Query().Get("nzbproxy_id") {
		t.Fatal("release identity changed between searches")
	}
	if count("good") != goodBefore {
		t.Fatal("search downloaded the NZB")
	}

	// Use the loopback port-forward while preserving the signed path and query.
	download := func(link *url.URL) error {
		body, err := s.request(proxy+link.RequestURI(), nil, 200)
		if err == nil && string(body) != fixtureNZB {
			return fmt.Errorf("NZB bytes differ from the fixture")
		}
		return err
	}
	var clients sync.WaitGroup
	for i := range 16 {
		link := []*url.URL{first, second}[i%2]
		clients.Go(func() {
			if err := download(link); err != nil {
				t.Error(err)
			}
		})
	}
	clients.Wait()
	if got := count("good") - goodBefore; got != 1 {
		t.Fatalf("concurrent downloads fetched the NZB %d times, want 1", got)
	}
	if got := grabs() - grabsBefore; got != 1 {
		t.Fatalf("concurrent downloads recorded %d Prowlarr grabs, want 1", got)
	}
	if err := download(s.search(t, proxy, id, "good-third")); err != nil {
		t.Fatal(err)
	}
	podUID := func() string {
		return run(t, "kubectl", "get", "pod", "-l", selector, "-o", "jsonpath={.items[0].metadata.uid}")
	}
	oldPod := podUID()
	rollout()
	waitForProxy(t)
	if uid := podUID(); uid == "" || oldPod == "" || uid == oldPod {
		t.Fatal("rollout did not replace the proxy pod")
	}
	proxy = forward(t, "nzb-proxy", "8080")
	if body := run(t, "kubectl", "exec", "fixture", "--", "wget", "-T", "10", "-qO-", first.String()); body != fixtureNZB {
		t.Fatal("replacement pod did not serve the signed link through its Service")
	}
	if err := download(s.search(t, proxy, id, "good-fourth")); err != nil {
		t.Fatal(err)
	}
	if count("good") != goodBefore+1 || grabs() != grabsBefore+1 {
		t.Fatal("cache reuse caused another provider fetch or Prowlarr grab")
	}
	path := first.RequestURI()
	s.mustRequest(t, proxy+strings.Replace(path, "apikey="+s.key, "apikey=wrong", 1), nil, 401)
	s.mustRequest(t, proxy+path+"&file=tampered", nil, 400)
	bad := s.search(t, proxy, id, "bad")
	for range 2 {
		s.mustRequest(t, proxy+bad.RequestURI(), nil, 502)
	}
	if got := count("bad") - badBefore; got != 1 {
		t.Fatalf("invalid NZB fetched %d times, want 1", got)
	}
	return proxy + first.RequestURI()
}
