package manager

import (
	"fmt"
	"net/url"
	"os"
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
func (s *Service) Recreate(in RecreateInput, pw ProgressWriter) (recreateErr error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return err
	}
	defer release()

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

	// Recreate deletes the volumes and the workspace before it builds again, so
	// if the build fails there is nothing left. A backup first makes that
	// recoverable.
	archivePath, err := s.backupBeforeDestroy(b, "recreate", in.NoBackup, pw)
	if err != nil {
		return err
	}

	pw.Printf("Recreating bench %q...\n", in.Name)
	s.TeardownBenchFiles(b)
	if err := s.RemoveBench(in.Name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	// Create writes a fresh bench record; carry the backup schedule over, or
	// a recreate would silently stop the bench's scheduled backups. The tunnel
	// is re-enabled rather than copied: its frpc.toml lived in the bench
	// directory that was just removed.
	schedule := b.BackupSchedule
	tun := b.Tunnel
	defer func() {
		if recreateErr != nil {
			if archivePath != "" {
				recreateErr = fmt.Errorf("%w\n\nThe bench's data was backed up first; get it back with:\n  ffm restore %s %s",
					recreateErr, archivePath, b.Name)
			}
			return
		}
		if schedule != nil {
			recreateErr = s.UpdateBench(b.Name, func(rec *state.Bench) { rec.BackupSchedule = schedule })
		}
		if recreateErr == nil && tun != nil && tun.Enabled {
			if err := s.TunnelEnable(TunnelEnableInput{
				BenchName: b.Name, ServerName: tun.Server, Subdomain: tun.Subdomain,
				// It was already published before the recreate.
				AllowDefaultPassword: true,
			}, pw); err != nil {
				fmt.Fprintf(pw.Stderr(), "warning: could not re-enable the tunnel: %v\n", err)
			}
		}
	}()

	return s.Create(CreateInput{
		recreating:        true,
		Name:              b.Name,
		FrappeBranch:      b.FrappeBranch,
		FrappeRepo:        b.FrappeRepo,
		Python:            b.Python,
		Node:              b.Node,
		Agent:             b.Agent,
		AgentReadOnly:     b.AgentReadOnly,
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
		Bind:              effectiveBind(b),
		SSHAgent:          b.SSHAgent && os.Getenv("SSH_AUTH_SOCK") != "",
		DomainAliases:     b.DomainAliases,
		AliasTLS:          b.AliasTLS,
	}, pw)
}

// effectiveBind turns a record's Bind (possibly empty on old records) into the
// explicit value that reproduces how its ports are published today.
func effectiveBind(b state.Bench) string {
	if b.PublishHost() == "" {
		return state.BindLAN
	}
	return state.BindLoopback
}
