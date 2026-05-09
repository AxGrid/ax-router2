package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/digitalocean"
)

// dnsACME wraps a certmagic.Config configured for DNS-01 challenges via a
// libdns provider (currently DigitalOcean).
//
// It supports both:
//   - Pre-issuance of an explicit list of hosts (AXR_ACME_DOMAINS), e.g.
//     "*.router.com,router.com".
//   - On-demand issuance for any (sub-)host under BaseDomain — covers
//     multi-level subdomains like "api.foo.router.com" that a single
//     wildcard cert cannot.
//
// The on-demand decision function whitelists hosts ending in
// ".<BaseDomain>" (or equal to BaseDomain) to avoid being weaponized to
// issue arbitrary certs.
type dnsACME struct {
	cfg     *certmagic.Config
	managed []string
	tlsCfg  *tls.Config

	startOnce sync.Once
}

func newDNSACME(c *Config) (*dnsACME, *tls.Config, error) {
	provider, err := buildLibdnsProvider(c)
	if err != nil {
		return nil, nil, err
	}

	storage := &certmagic.FileStorage{Path: c.TLS.DNSCache}
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(cert certmagic.Certificate) (*certmagic.Config, error) {
			return certmagic.NewDefault(), nil
		},
	})
	cmCfg := certmagic.New(cache, certmagic.Config{
		Storage: storage,
	})

	caURL := certmagic.LetsEncryptProductionCA
	if c.TLS.DNSStaging {
		caURL = certmagic.LetsEncryptStagingCA
	}

	issuer := certmagic.NewACMEIssuer(cmCfg, certmagic.ACMEIssuer{
		CA:     caURL,
		Email:  c.TLS.DNSEmail,
		Agreed: true,
		DNS01Solver: &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{
				DNSProvider: provider,
			},
		},
	})
	cmCfg.Issuers = []certmagic.Issuer{issuer}

	base := strings.ToLower(c.BaseDomain)
	cmCfg.OnDemand = &certmagic.OnDemandConfig{
		DecisionFunc: func(ctx context.Context, name string) error {
			n := strings.ToLower(name)
			if n == base || strings.HasSuffix(n, "."+base) {
				return nil
			}
			return fmt.Errorf("name %q not allowed by router base %q", name, base)
		},
	}

	return &dnsACME{
		cfg:     cmCfg,
		managed: c.TLS.DNSManagedHosts,
		tlsCfg:  cmCfg.TLSConfig(),
	}, cmCfg.TLSConfig(), nil
}

// start triggers any pre-issuance and lets certmagic begin its renewal loop.
// Safe to call multiple times.
func (d *dnsACME) start(ctx context.Context) error {
	var err error
	d.startOnce.Do(func() {
		if len(d.managed) > 0 {
			err = d.cfg.ManageAsync(ctx, d.managed)
		}
	})
	return err
}

// buildLibdnsProvider returns the libdns provider implementation matching
// AXR_DNS_PROVIDER. New providers can be added with one extra case here and
// one extra import.
func buildLibdnsProvider(c *Config) (certmagic.DNSProvider, error) {
	switch strings.ToLower(c.TLS.DNSProvider) {
	case "digitalocean":
		return &digitalocean.Provider{APIToken: c.TLS.DigitalOceanToken}, nil
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", c.TLS.DNSProvider)
	}
}
