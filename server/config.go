package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config drives the public-facing server, the control listener and TLS.
//
// Loaded from .env via env vars (see .env.example). All durations parsed by
// time.ParseDuration.
type Config struct {
	// Public HTTP listener (where browsers / external clients connect).
	PublicAddr string // e.g. ":80"
	// Optional TLS-enabled public listener. Empty = TLS off.
	PublicTLSAddr string // e.g. ":443"

	// Control TCP listener — where router-clients connect.
	ControlAddr string // e.g. ":7000"

	// BaseDomain — the apex. Service names are subdomain labels under it; the
	// apex itself is reserved for the status dashboard and cannot be claimed
	// by any service. Service name is the last label of Host after stripping
	// ".<BaseDomain>".
	//
	// Example: BaseDomain="router.com", request to "api.foo.router.com"
	//          → service_name = "foo".
	BaseDomain string

	// ReconnectGrace — how long to hold inbound requests waiting for a
	// previously-connected client to come back, before returning 502.
	ReconnectGrace time.Duration

	// Tokens map: token -> service_name. One token may register only its
	// assigned service. Use "*" as the value to grant a token the right to
	// register *any* (valid) service name supplied in the Hello frame.
	//
	// Loaded either inline (AXR_TOKENS) or from a JSON file (AXR_TOKENS_FILE).
	Tokens map[string]string
	// TokensFile — path watched by fsnotify for hot-reload (optional).
	TokensFile string

	// Admin (status page) basic auth. Empty user OR pass = page is public.
	AdminUser string
	AdminPass string

	// HTTPSRedirect — when true, the plain-HTTP listener 301-redirects every
	// request to its HTTPS counterpart. ACME HTTP-01 challenges and the
	// "Issuing certificate" page are exempted automatically. No-op unless a
	// TLS listener is also active (AXR_PUBLIC_TLS_ADDR + a non-off TLS mode).
	HTTPSRedirect bool

	// TLS settings.
	TLS TLSConfig
}

type TLSMode string

const (
	TLSOff      TLSMode = "off"
	TLSFile     TLSMode = "file"
	TLSAutocert TLSMode = "autocert" // ACME HTTP-01 / TLS-ALPN-01, no wildcards
	TLSDNS      TLSMode = "dns"      // ACME DNS-01 via libdns provider; supports wildcards
)

type TLSConfig struct {
	Mode TLSMode

	// Mode=file
	CertFile string
	KeyFile  string

	// Mode=autocert (Let's Encrypt). HTTP-01/TLS-ALPN-01 do NOT support
	// wildcard certs — list explicit hostnames here.
	AutocertDomains []string
	AutocertEmail   string
	AutocertCache   string // directory

	// Mode=dns. DNS-01 challenges via a libdns provider; supports wildcards
	// AND on-demand issuance for any depth of subdomain under BaseDomain.
	DNSProvider     string   // "digitalocean" (currently the only one)
	DNSEmail        string
	DNSCache        string   // directory
	DNSManagedHosts []string // explicit list to pre-issue (e.g. "*.router.com,router.com"); empty = on-demand only
	DNSStaging      bool     // use Let's Encrypt staging
	// Provider-specific creds; populated from env, e.g. DIGITALOCEAN_TOKEN.
	DigitalOceanToken string
}

// LoadConfig reads env (already populated, e.g. by godotenv) and returns Config.
func LoadConfig() (*Config, error) {
	c := &Config{
		PublicAddr:     env("AXR_PUBLIC_ADDR", ":80"),
		PublicTLSAddr:  env("AXR_PUBLIC_TLS_ADDR", ""),
		ControlAddr:    env("AXR_CONTROL_ADDR", ":7000"),
		BaseDomain:     strings.ToLower(env("AXR_BASE_DOMAIN", "")),
		ReconnectGrace: envDuration("AXR_RECONNECT_GRACE", 3*time.Second),
		Tokens:         map[string]string{},
		TokensFile:     env("AXR_TOKENS_FILE", ""),
		AdminUser:      env("AXR_ADMIN_USER", ""),
		AdminPass:      env("AXR_ADMIN_PASS", ""),
		HTTPSRedirect:  envBool("AXR_HTTPS_REDIRECT", false),
		TLS: TLSConfig{
			Mode:              TLSMode(strings.ToLower(env("AXR_TLS_MODE", "off"))),
			CertFile:          env("AXR_TLS_CERT_FILE", ""),
			KeyFile:           env("AXR_TLS_KEY_FILE", ""),
			AutocertDomains:   splitCSV(env("AXR_TLS_AUTOCERT_DOMAINS", "")),
			AutocertEmail:     env("AXR_TLS_AUTOCERT_EMAIL", ""),
			AutocertCache:     env("AXR_TLS_AUTOCERT_CACHE", "./autocert-cache"),
			DNSProvider:       strings.ToLower(env("AXR_DNS_PROVIDER", "")),
			DNSEmail:          env("AXR_ACME_EMAIL", ""),
			DNSCache:          env("AXR_ACME_CACHE", "./acme-cache"),
			DNSManagedHosts:   splitCSV(env("AXR_ACME_DOMAINS", "")),
			DNSStaging:        envBool("AXR_ACME_STAGING", false),
			DigitalOceanToken: env("DIGITALOCEAN_TOKEN", env("DO_AUTH_TOKEN", "")),
		},
	}

	if c.BaseDomain == "" {
		return nil, fmt.Errorf("AXR_BASE_DOMAIN is required")
	}

	if inline := env("AXR_TOKENS", ""); inline != "" {
		// Format: token1:service1,token2:service2 (use "*" as service for "any").
		for _, pair := range strings.Split(inline, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			i := strings.IndexByte(pair, ':')
			if i <= 0 {
				return nil, fmt.Errorf("AXR_TOKENS: bad pair %q", pair)
			}
			c.Tokens[pair[:i]] = strings.ToLower(strings.TrimSpace(pair[i+1:]))
		}
	}
	if c.TokensFile != "" {
		if err := readTokensFile(c.TokensFile, c.Tokens); err != nil {
			return nil, err
		}
	}
	if len(c.Tokens) == 0 {
		return nil, fmt.Errorf("no tokens configured (set AXR_TOKENS or AXR_TOKENS_FILE)")
	}

	switch c.TLS.Mode {
	case "", TLSOff:
		c.TLS.Mode = TLSOff
	case TLSFile:
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return nil, fmt.Errorf("AXR_TLS_MODE=file requires AXR_TLS_CERT_FILE and AXR_TLS_KEY_FILE")
		}
	case TLSAutocert:
		// AXR_TLS_AUTOCERT_DOMAINS is optional: dynamic per-service issuance
		// handles "<service>.<base>" automatically once a client registers.
		// An empty list is valid and common.
	case TLSDNS:
		if c.TLS.DNSProvider == "" {
			return nil, fmt.Errorf("AXR_TLS_MODE=dns requires AXR_DNS_PROVIDER")
		}
		switch c.TLS.DNSProvider {
		case "digitalocean":
			if c.TLS.DigitalOceanToken == "" {
				return nil, fmt.Errorf("DIGITALOCEAN_TOKEN is required for AXR_DNS_PROVIDER=digitalocean")
			}
		default:
			return nil, fmt.Errorf("unsupported AXR_DNS_PROVIDER=%q (currently: digitalocean)", c.TLS.DNSProvider)
		}
	default:
		return nil, fmt.Errorf("unknown AXR_TLS_MODE=%q", c.TLS.Mode)
	}

	return c, nil
}

// readTokensFile reads JSON {"token":"service_or_star"} into m, replacing any
// previous file-derived entries. (Inline AXR_TOKENS entries are merged in
// LoadConfig and survive reloads.)
func readTokensFile(path string, m map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read tokens file: %w", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse tokens file: %w", err)
	}
	for k, v := range parsed {
		m[k] = strings.ToLower(strings.TrimSpace(v))
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
