package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	env := map[string]string{"NZB_PROXY_UPSTREAM_URL": "http://prowlarr:9696", "NZB_PROXY_PUBLIC_URL": "http://nzb-proxy:8080", "NZB_PROXY_API_KEY": "secret"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.CacheTTL != 168*time.Hour || c.MaxNZBBytes != 32<<20 {
		t.Fatalf("unexpected defaults: retention=%s max_nzb_bytes=%d", c.CacheTTL, c.MaxNZBBytes)
	}
	for _, tc := range []struct{ key, value string }{
		{"NZB_PROXY_CACHE_TTL", "0s"}, {"NZB_PROXY_FETCH_TIMEOUT", "-1s"}, {"NZB_PROXY_MAX_NZB_BYTES", "9223372036854775807"},
		{"NZB_PROXY_PUBLIC_URL", "http://user:password@host"}, {"NZB_PROXY_UPSTREAM_URL", "http://host?apikey=secret"}, {"NZB_PROXY_ALLOWED_HOSTS", "*.example.com"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := Load(func(k string) string {
				if k == tc.key {
					return tc.value
				}
				return env[k]
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") {
				t.Fatal("error leaks secret")
			}
		})
	}
}

func TestExampleAllowlistsUseReservedHosts(t *testing.T) {
	for name, prefix := range map[string]string{
		"deploy/compose/.env.example": "NZB_PROXY_ALLOWED_HOSTS=",
		"deploy/compose/compose.yaml": "NZB_PROXY_ALLOWED_HOSTS: ${NZB_PROXY_ALLOWED_HOSTS:-",
		"README.md":                   "export NZB_PROXY_ALLOWED_HOSTS=",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				found = true
				for _, host := range strings.Split(strings.TrimSuffix(value, "}"), ",") {
					if !strings.HasSuffix(host, ".example") {
						t.Errorf("%s contains a non-example download host", name)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s is missing the example allowlist", name)
		}
	}
}
