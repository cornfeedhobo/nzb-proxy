// Package config reads the service's environment configuration.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen       string
	UpstreamURL  *url.URL
	PublicURL    *url.URL
	APIKey       string
	AllowedHosts []string
	CacheDir     string
	CacheTTL     time.Duration
	FetchTimeout time.Duration
	MaxNZBBytes  int64
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{Listen: ":8080", CacheDir: "./data", CacheTTL: 7 * 24 * time.Hour, FetchTimeout: 60 * time.Second, MaxNZBBytes: 32 << 20}
	for key, target := range map[string]*string{"NZB_PROXY_LISTEN": &c.Listen, "NZB_PROXY_CACHE_DIR": &c.CacheDir} {
		if v := getenv(key); v != "" {
			*target = v
		}
	}
	var err error
	c.UpstreamURL, err = parseURL(getenv("NZB_PROXY_UPSTREAM_URL"))
	if err != nil {
		return c, fmt.Errorf("NZB_PROXY_UPSTREAM_URL: %w", err)
	}
	c.PublicURL, err = parseURL(getenv("NZB_PROXY_PUBLIC_URL"))
	if err != nil {
		return c, fmt.Errorf("NZB_PROXY_PUBLIC_URL: %w", err)
	}
	c.APIKey = strings.TrimSpace(getenv("NZB_PROXY_API_KEY"))
	if c.APIKey == "" {
		return c, fmt.Errorf("NZB_PROXY_API_KEY is required")
	}
	for _, host := range strings.Split(getenv("NZB_PROXY_ALLOWED_HOSTS"), ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, "/:@?#* ") {
			return c, fmt.Errorf("NZB_PROXY_ALLOWED_HOSTS must contain exact hostnames without ports or schemes")
		}
		c.AllowedHosts = append(c.AllowedHosts, host)
	}
	for key, target := range map[string]*time.Duration{"NZB_PROXY_CACHE_TTL": &c.CacheTTL, "NZB_PROXY_FETCH_TIMEOUT": &c.FetchTimeout} {
		if v := getenv(key); v != "" {
			*target, err = time.ParseDuration(v)
			if err != nil || *target <= 0 {
				return c, fmt.Errorf("%s must be a positive duration such as 24h", key)
			}
		}
	}
	if v := getenv("NZB_PROXY_MAX_NZB_BYTES"); v != "" {
		c.MaxNZBBytes, err = strconv.ParseInt(v, 10, 64)
		if err != nil || c.MaxNZBBytes <= 0 || c.MaxNZBBytes > 1<<30 {
			return c, fmt.Errorf("NZB_PROXY_MAX_NZB_BYTES must be between 1 and 1073741824")
		}
	}
	return c, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}
