package config

import (
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
