package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/acme/autocert"
)

// buildTLS turns Config.TLS into a *tls.Config or returns nil if TLS is off.
//
// Returns (tlsConfig, autocertManager, dnsACME, err). At most one of
// autocertManager / dnsACME is non-nil.
//
//   - autocertManager is non-nil only for TLSAutocert mode; the caller should
//     mount its HTTPHandler on port 80 (or fall back to TLS-ALPN-01).
//   - dnsACME is non-nil only for TLSDNS mode; it owns its own renewal loop
//     and serves certs via the returned *tls.Config.
//
// `reg` is consulted by the autocert HostPolicy so that certs are only
// issued for service names that have actually been registered (protects
// the LE rate limit budget).
func buildTLS(c *Config, reg *Registry) (cfg *tls.Config, mgr *autocert.Manager, dns *dnsACME, err error) {
	switch c.TLS.Mode {
	case TLSOff, "":
		return nil, nil, nil, nil
	case TLSFile:
		cert, err := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load cert/key: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil, nil, nil
	case TLSAutocert:
		m := &autocert.Manager{
			Cache:      autocert.DirCache(c.TLS.AutocertCache),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocertHostPolicy(c, reg),
			Email:      c.TLS.AutocertEmail,
		}
		return m.TLSConfig(), m, nil, nil
	case TLSDNS:
		d, tlsCfg, err := newDNSACME(c)
		if err != nil {
			return nil, nil, nil, err
		}
		return tlsCfg, nil, d, nil
	default:
		return nil, nil, nil, fmt.Errorf("unknown TLS mode %q", c.TLS.Mode)
	}
}

// autocertHostPolicy accepts:
//   - hosts explicitly listed in AXR_TLS_AUTOCERT_DOMAINS
//   - the apex (BaseDomain) and "www.<base>"
//   - "<service>.<base>" where <service> matches serviceNameRE *and* a
//     session has ever been registered for it. Holding the gate at "ever
//     registered" keeps the LE rate-limit budget safe from random probes.
func autocertHostPolicy(c *Config, reg *Registry) autocert.HostPolicy {
	base := strings.ToLower(c.BaseDomain)
	suffix := "." + base
	allow := make(map[string]struct{}, len(c.TLS.AutocertDomains))
	for _, d := range c.TLS.AutocertDomains {
		allow[strings.ToLower(d)] = struct{}{}
	}
	allow[base] = struct{}{}
	allow["www."+base] = struct{}{}

	return func(_ context.Context, host string) error {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if _, ok := allow[host]; ok {
			return nil
		}
		if !strings.HasSuffix(host, suffix) {
			return errors.New("acme/autocert: host not allowed")
		}
		inner := host[:len(host)-len(suffix)]
		// Service-name shape (single label).
		if !serviceNameRE.MatchString(inner) {
			return errors.New("acme/autocert: host not allowed")
		}
		// Multi-level subdomains (api.foo.base) are NOT covered — autocert
		// can't wildcard. Only the bare service host is allowed.
		if reg != nil && !reg.HasService(inner) {
			return errors.New("acme/autocert: service not registered")
		}
		return nil
	}
}
