package manager

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// tlsModeFor maps create's mode/no-ssl pair onto the persisted TLSMode value.
func tlsModeFor(mode string, noSSL bool) string {
	if mode != "prod" {
		return ""
	}
	if noSSL {
		return state.TLSNone
	}
	return state.TLSLetsEncrypt
}

// prodNoSSL reports whether a prod bench serves its primary domain over plain
// HTTP. Prefers the recorded TLSMode and falls back to inferring it from
// ProxyHost for records written before that field existed.
func prodNoSSL(b state.Bench) bool {
	switch b.TLSMode {
	case state.TLSNone:
		return true
	case state.TLSLetsEncrypt:
		return false
	}
	return inferProdNoSSL(b)
}

// primaryHostOf returns the hostname the bench's main Traefik router matches.
func primaryHostOf(b state.Bench) string {
	if b.IsProd() {
		return b.Domain
	}
	return b.Name + ".localhost"
}

// normalizeAliases validates, lower-cases, and de-duplicates a list of alias
// hostnames, rejecting any that collides with the bench's own primary host.
func normalizeAliases(raw []string, primaryHost string) ([]string, error) {
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		d, err := bench.NormalizeDomain(r)
		if err != nil {
			return nil, err
		}
		if d == primaryHost {
			return nil, fmt.Errorf("domain %q is already this bench's primary host", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// checkAliasesFree rejects hostnames already claimed by another bench. Two
// Traefik routers matching the same Host() are not an error to Docker or to
// Traefik — traffic just lands on whichever router wins, which is far harder to
// diagnose later than an upfront refusal.
func (s *Service) checkAliasesFree(aliases []string, selfName string) error {
	benches, err := s.LoadBenches()
	if err != nil {
		return err
	}
	for _, other := range benches {
		if other.Name == selfName {
			continue
		}
		taken := append([]string{primaryHostOf(other)}, other.DomainAliases...)
		for _, a := range aliases {
			if slices.Contains(taken, a) {
				return fmt.Errorf("domain %q is already routed to bench %q", a, other.Name)
			}
		}
	}
	return nil
}

// composeDataFor rebuilds the ComposeData for an existing bench so that
// docker-compose.yml can be regenerated in place. Fields that were not recorded
// at create time fall back to the same defaults Create applies.
func (s *Service) composeDataFor(b state.Bench) (bench.ComposeData, error) {
	hostUID, hostGID, err := composeUserIDs(b.MatchHostUser)
	if err != nil {
		return bench.ComposeData{}, err
	}
	mode := b.Mode
	if mode == "" {
		mode = "dev"
	}
	data := bench.ComposeData{
		Name:              b.Name,
		Mode:              mode,
		BenchDir:          b.Dir,
		HostUID:           hostUID,
		HostGID:           hostGID,
		WebPort:           b.WebPort,
		WebPortEnd:        b.WebPort + 5,
		SocketIOPort:      b.SocketIOPort,
		SocketIOPortEnd:   b.SocketIOPort + 5,
		DBType:            b.DBEngine(),
		DBRootPassword:    b.DBPassword,
		ForwardSSHAgent:   mode == "dev" && os.Getenv("SSH_AUTH_SOCK") != "",
		Domain:            b.Domain,
		SiteName:          b.SiteName,
		NoSSL:             mode == "prod" && prodNoSSL(b),
		MariaDBBufferPool: b.MariaDBBufferPool,
		GunicornWorkers:   b.GunicornWorkers,
		WorkerLongCount:   b.WorkerLongCount,
		WorkerShortCount:  b.WorkerShortCount,
		RedisCacheMaxmem:  b.RedisCacheMaxmem,
		RedisQueueMaxmem:  b.RedisQueueMaxmem,
		SlowQueryLog:      b.SlowQueryLog,
		DomainAliases:     b.DomainAliases,
		AliasTLS:          b.AliasTLS,
	}
	if data.MariaDBBufferPool == "" {
		data.MariaDBBufferPool = "1G"
	}
	if data.GunicornWorkers <= 0 {
		data.GunicornWorkers = 2
	}
	if data.WorkerLongCount <= 0 {
		data.WorkerLongCount = 1
	}
	if data.WorkerShortCount <= 0 {
		data.WorkerShortCount = 1
	}
	if data.RedisCacheMaxmem == "" {
		data.RedisCacheMaxmem = "512mb"
	}
	if data.RedisQueueMaxmem == "" {
		data.RedisQueueMaxmem = "512mb"
	}
	return data, nil
}

// DomainList returns the bench's primary host followed by its aliases.
func (s *Service) DomainList(name string) (primary string, aliases []string, err error) {
	b, err := s.GetBench(name)
	if err != nil {
		return "", nil, err
	}
	return primaryHostOf(b), b.DomainAliases, nil
}

// DomainAdd routes an extra hostname to a bench.
func (s *Service) DomainAdd(in DomainInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	d, err := bench.NormalizeDomain(in.Domain)
	if err != nil {
		return err
	}
	if d == primaryHostOf(b) {
		return fmt.Errorf("domain %q is already this bench's primary host", d)
	}
	aliasTLS := b.AliasTLS || in.TLS
	if slices.Contains(b.DomainAliases, d) && aliasTLS == b.AliasTLS {
		pw.Printf("Domain %q is already routed to bench %q — nothing to do.\n", d, b.Name)
		return nil
	}
	if err := s.checkAliasesFree([]string{d}, b.Name); err != nil {
		return err
	}
	if in.TLS {
		if b.IsDev() {
			return fmt.Errorf("--tls applies to production benches only (dev benches are served over plain HTTP)")
		}
		if prodNoSSL(b) {
			return fmt.Errorf("bench %q serves its primary domain over plain HTTP (--no-ssl), so its aliases cannot use Let's Encrypt", b.Name)
		}
	}

	aliases := b.DomainAliases
	if !slices.Contains(aliases, d) {
		aliases = append(append([]string(nil), aliases...), d)
	}
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.DomainAliases = aliases
		rec.AliasTLS = aliasTLS
	}); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	b.DomainAliases = aliases
	b.AliasTLS = aliasTLS

	pw.Printf("Routing %q to bench %q...\n", d, b.Name)
	if in.TLS {
		pw.Printf("  Note: Let's Encrypt must be able to reach %s over the public internet on port 80,\n"+
			"  or the certificate order fails and the alias stays unreachable over HTTPS.\n", d)
	}
	if err := s.applyDomainChange(b, pw); err != nil {
		return err
	}
	printDomainHints(pw, b, d)
	return nil
}

// DomainRemove stops routing a hostname to a bench.
func (s *Service) DomainRemove(in DomainInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	d, err := bench.NormalizeDomain(in.Domain)
	if err != nil {
		return err
	}
	if !slices.Contains(b.DomainAliases, d) {
		return fmt.Errorf("bench %q has no domain alias %q", b.Name, d)
	}

	aliases := slices.DeleteFunc(append([]string(nil), b.DomainAliases...),
		func(a string) bool { return a == d })
	// With no aliases left there is nothing for AliasTLS to apply to; clearing it
	// stops a later `ffm domain add` from inheriting HTTPS the user never asked
	// for on the new hostname.
	aliasTLS := b.AliasTLS && len(aliases) > 0
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.DomainAliases = aliases
		rec.AliasTLS = aliasTLS
	}); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	b.DomainAliases = aliases
	b.AliasTLS = aliasTLS

	pw.Printf("Removing %q from bench %q...\n", d, b.Name)
	if err := s.applyDomainChange(b, pw); err != nil {
		return err
	}
	pw.Printf("Done. %q no longer routes to bench %q.\n", d, b.Name)
	return nil
}

// applyDomainChange regenerates docker-compose.yml from the updated state and
// rolls the change onto the running containers.
//
// This is deliberately not `ffm recreate`: Traefik reads its routes from
// container labels, so the containers must be replaced, but the bench's data
// lives in named volumes and the ./workspace bind mount and is untouched by a
// `docker compose up -d`.
func (s *Service) applyDomainChange(b state.Bench, pw ProgressWriter) error {
	data, err := s.composeDataFor(b)
	if err != nil {
		return err
	}
	if err := proxy.EnsureNetwork(); err != nil {
		return fmt.Errorf("ensure proxy network: %w", err)
	}
	if err := bench.WriteCompose(b.Dir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	pw.Println("  ✓ docker-compose.yml updated")

	// Socket.IO auth resolves the site from the request Host unless this patch is
	// in place, so re-apply it before the containers come back up.
	if err := bench.PatchAuthenticateJs(b.Dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
	}

	if s.LiveStatus(b) != "running" {
		pw.Println("  Bench is not running — the new routing applies on the next 'ffm start'.")
		return nil
	}

	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	pw.Println("  ✓ containers updated")

	if b.IsDev() {
		// `up -d` replaced the frappe container, and on a dev bench the honcho
		// stack is started by exec rather than by the container command, so it
		// has to be relaunched explicitly.
		restartDevServer(runner, pw)
	} else {
		// Prod runs socket.io in its own container whose command survives the
		// replacement; restart it so it re-reads the patched authenticate.js.
		if err := runner.RestartService("socketio"); err != nil && s.Verbose {
			pw.Printf("  warning: could not restart socketio: %v\n", err)
		}
	}
	return nil
}

// restartDevServer relaunches the honcho stack inside a dev bench's frappe
// container. pkill exits non-zero when nothing matched, which is not an error.
func restartDevServer(runner *bench.Runner, pw ProgressWriter) {
	pw.Println("  Restarting dev server...")
	cmd := "pkill -f 'honcho start' 2>/dev/null; sleep 1" +
		" && cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"
	if _, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
		pw.Println("  (dev server restart returned non-zero — it may already have been stopped)")
	}
	pw.Println("  ✓ Dev server restarting in background")
}

// printDomainHints explains the two things ffm cannot do for the user: point DNS
// at this host, and make sure the Socket.IO path is reachable.
func printDomainHints(pw ProgressWriter, b state.Bench, d string) {
	scheme := "http"
	if b.IsProd() && b.AliasTLS {
		scheme = "https"
	}
	pw.Printf("\nBench %q now answers on %s://%s\n", b.Name, scheme, d)

	if !proxy.IsRunning() {
		pw.Println("  ! The shared Traefik proxy is not running — start it with 'ffm proxy start'.")
	}

	ip := hostLANIP()
	shown := ip
	if shown == "" {
		shown = "<this-host-ip>"
	}
	pw.Println("\nPoint DNS at this host so other machines resolve the name:")
	pw.Printf("  • Router / local DNS server (reaches every device):  %s  %s\n", shown, d)
	pw.Printf("  • dnsmasq or Pi-hole:  address=/%s/%s\n", d, shown)
	pw.Printf("  • Per-machine /etc/hosts line:  %s  %s\n", shown, d)
	if ip == "" {
		pw.Println("  (could not detect this host's LAN address — substitute it yourself)")
	}
	if warning := domainNameWarning(d); warning != "" {
		pw.Printf("  Note: %s\n", warning)
	}

	if b.IsDev() {
		pw.Printf("\nSocket.IO connects straight to %s:%d (the published port), not through Traefik,\n"+
			"  so that port must be reachable from the other machines too.\n", d, b.SocketIOPort)
		if !bench.RealtimeAcceptsAnyHost(b.Dir) {
			pw.Println("  ! Could not patch realtime/middlewares/authenticate.js, so Socket.IO will reject")
			pw.Println("    connections on this hostname with \"Invalid namespace\". Page loads still work.")
		}
	}
}

// publicTLDs are suffixes commonly grabbed for LAN use that are in fact
// delegated on the public internet. Pointing one at a private address shadows
// every real site under it for every device using that DNS server.
var publicTLDs = []string{".cc", ".dev", ".app", ".io", ".co", ".sh", ".ai", ".tv", ".me", ".ws"}

// domainNameWarning returns advice about a hostname that will behave badly as a
// LAN name, or an empty string when the name is a sound choice. .test, .invalid,
// .example and .internal are all reserved for exactly this use and pass clean.
func domainNameWarning(d string) string {
	if !strings.Contains(d, ".") {
		return fmt.Sprintf("%q has no dot, so browsers and resolvers may treat it as a search term "+
			"rather than a hostname — prefer something like %s.test.", d, d)
	}
	if strings.HasSuffix(d, ".local") {
		return fmt.Sprintf("%q is in the mDNS/Bonjour namespace, which is resolved by multicast rather "+
			"than by your DNS server — most clients will never query DNS for it. Prefer a .test name.", d)
	}
	for _, t := range publicTLDs {
		if strings.HasSuffix(d, t) {
			return fmt.Sprintf("%s is a real public TLD. Overriding %q locally hides the genuine domain "+
				"from every device on this network — prefer a .test or .internal name, or a subdomain "+
				"of a domain you own.", t, d)
		}
	}
	return ""
}

// hostLANIP returns the address this host presents on its default route.
// The UDP "connection" is routing-table lookup only — no packet is sent.
func hostLANIP() string {
	conn, err := net.Dial("udp", "203.0.113.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}
