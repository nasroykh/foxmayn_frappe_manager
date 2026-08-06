package manager

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func inferProdNoSSL(b state.Bench) bool {
	return b.IsProd() && strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.ProxyHost)), "http://")
}

func devProxyArgsForRecreate(b state.Bench, portOverride *int, hostOverride *string) (proxyPort int, proxyHost string, err error) {
	raw := strings.TrimSpace(b.ProxyHost)
	var derivedPort int
	var derivedHost string
	if raw != "" {
		u, perr := url.Parse(raw)
		if perr != nil {
			if hostOverride == nil && portOverride == nil {
				return 0, "", fmt.Errorf("parse stored proxy_host %q: %w", raw, perr)
			}
		} else {
			derivedHost = u.Hostname()
			if derivedHost == "" && hostOverride == nil {
				return 0, "", fmt.Errorf("stored proxy_host %q has no hostname; pass --proxy-host", raw)
			}
			switch strings.ToLower(u.Scheme) {
			case "https":
				derivedPort = 443
			default:
				derivedPort = 80
			}
		}
	}
	proxyPort = derivedPort
	proxyHost = derivedHost
	if portOverride != nil {
		proxyPort = *portOverride
	}
	if hostOverride != nil {
		proxyHost = strings.TrimPrefix(strings.TrimPrefix(*hostOverride, "https://"), "http://")
	}
	return proxyPort, proxyHost, nil
}

// Recreate tears down and reprovisions a bench from saved state.
func (s *Service) Recreate(in RecreateInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}

	mode := b.Mode
	if mode == "" {
		mode = "dev"
	}
	apps := append([]string(nil), b.Apps...)

	proxyPortInt := 0
	proxyHostStr := ""
	if b.IsDev() {
		var derr error
		proxyPortInt, proxyHostStr, derr = devProxyArgsForRecreate(b, in.ProxyPortOverride, in.ProxyHostOverride)
		if derr != nil {
			return derr
		}
	}

	noSSL := b.IsProd() && prodNoSSL(b)
	acmeEmail := ""

	var fixedWeb, fixedSio int
	if !in.ReallocatePorts && bench.ValidBenchPortPair(b.WebPort, b.SocketIOPort) {
		fixedWeb = b.WebPort
		fixedSio = b.SocketIOPort
	}

	// Reuse the recorded tuning rather than the create-time defaults, so a bench
	// built with e.g. --gunicorn-workers 8 does not silently come back with 2.
	// Empty/zero values fall through to Create's defaults, which is the right
	// behaviour for records written before these fields were persisted.
	mariadbBufferPool := b.MariaDBBufferPool
	if mariadbBufferPool == "" && b.IsProd() {
		mariadbBufferPool = "1G"
	}

	pw.Printf("Recreating bench %q...\n", in.Name)
	s.TeardownBenchFiles(b)
	if err := s.RemoveBench(in.Name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	return s.Create(CreateInput{
		Name:              b.Name,
		FrappeBranch:      b.FrappeBranch,
		FrappeRepo:        b.FrappeRepo,
		Apps:              apps,
		AdminPassword:     b.AdminPassword,
		DBPassword:        b.DBPassword,
		DBType:            b.DBEngine(),
		GithubToken:       in.GithubToken,
		ProxyPort:         proxyPortInt,
		ProxyHost:         proxyHostStr,
		Mode:              mode,
		Domain:            b.Domain,
		NoSSL:             noSSL,
		AcmeEmail:         acmeEmail,
		MariaDBBufferPool: mariadbBufferPool,
		GunicornWorkers:   b.GunicornWorkers,
		WorkerLongCount:   b.WorkerLongCount,
		WorkerShortCount:  b.WorkerShortCount,
		RedisCacheMaxmem:  b.RedisCacheMaxmem,
		RedisQueueMaxmem:  b.RedisQueueMaxmem,
		SlowQueryLog:      b.SlowQueryLog,
		FixedWebPort:      fixedWeb,
		FixedSocketIOPort: fixedSio,
		MatchHostUser:     b.MatchHostUser,
		DomainAliases:     b.DomainAliases,
		AliasTLS:          b.AliasTLS,
	}, pw)
}
