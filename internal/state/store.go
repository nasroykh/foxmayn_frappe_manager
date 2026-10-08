package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/lock"
)

// TLS modes recorded in Bench.TLSMode.
const (
	// TLSLetsEncrypt serves the primary domain over HTTPS with certificates
	// issued by Traefik's Let's Encrypt resolver.
	TLSLetsEncrypt = "letsencrypt"
	// TLSNone serves the primary domain over plain HTTP (`--no-ssl`), leaving
	// TLS termination to an external proxy.
	TLSNone = "none"
)

// TunnelState holds the tunnel configuration for a bench.
// Zero value means no tunnel configured.
type TunnelState struct {
	// Server is the tunnel server profile name from tunnel.json.
	Server string `json:"server,omitempty"`
	// Subdomain is the public subdomain slug (e.g. "mydev" → mydev.tunnel.example.com).
	Subdomain string `json:"subdomain,omitempty"`
	Enabled   bool   `json:"enabled,omitempty"`
}

// BackupPolicy is a bench's scheduled-backup and retention policy.
//
// Retention is tiered: of the scheduled archives, the newest one in each of
// the last KeepHourly hours, KeepDaily days and KeepWeekly ISO weeks is kept,
// plus a fixed floor of the newest few whatever their age.
type BackupPolicy struct {
	Enabled    bool `json:"enabled"`
	EveryHours int  `json:"every_hours"`
	KeepHourly int  `json:"keep_hourly,omitempty"`
	KeepDaily  int  `json:"keep_daily,omitempty"`
	KeepWeekly int  `json:"keep_weekly,omitempty"`
	// Files is how often a scheduled run includes attachments:
	// "every-run", "daily", "weekly" or "never".
	Files string `json:"files,omitempty"`
	// Encrypt encrypts scheduled archives with age (ffm backup key init).
	Encrypt bool `json:"encrypt,omitempty"`
}

// Bench holds the persisted state for a single managed bench.
type Bench struct {
	Name         string `json:"name"`
	Dir          string `json:"dir"`
	WebPort      int    `json:"web_port"`
	SocketIOPort int    `json:"socketio_port"`
	FrappeBranch string `json:"frappe_branch"`
	// FrappeRepo is the custom --frappe-repo source (URL, optionally with an
	// @branch suffix) used at create time. Empty means the official
	// frappe/frappe repo was used. Persisted so Recreate reuses the same fork
	// instead of silently falling back to the official repo.
	FrappeRepo string `json:"frappe_repo,omitempty"`
	// Python ("3.12") and Node ("22") are the toolchain bench init and the
	// image were set up with. Empty on benches created before ffm chose a
	// toolchain per branch: those run on the image defaults, and a rebuild
	// keeps them there rather than swapping Node under existing node_modules.
	Python        string `json:"python,omitempty"`
	Node          string `json:"node,omitempty"`
	AdminPassword string `json:"admin_password"`
	DBPassword    string `json:"db_password"`
	// DBType is "mariadb" or "postgres". Empty is treated as "mariadb" for backward compatibility.
	DBType   string   `json:"db_type,omitempty"`
	SiteName string   `json:"site_name"`
	Apps     []string `json:"apps"`
	// ProxyHost is the public URL when the bench is running behind a reverse
	// proxy (e.g. "https://frappe.example.com"). Empty means direct access.
	ProxyHost string `json:"proxy_host,omitempty"`
	// Mode is "dev" or "prod". Empty is treated as "dev" for backward compatibility.
	Mode string `json:"mode,omitempty"`
	// Domain is the public domain for production benches (e.g. "erp.example.com").
	Domain string `json:"domain,omitempty"`
	// DomainAliases are extra hostnames Traefik routes to this bench on top of
	// the mode's primary host. Managed by `ffm domain add` / `remove`.
	DomainAliases []string `json:"domain_aliases,omitempty"`
	// AliasTLS serves the aliases over HTTPS with Let's Encrypt instead of plain
	// HTTP. Prod + SSL only; requires every alias to be publicly resolvable.
	AliasTLS bool `json:"alias_tls,omitempty"`
	// TLSMode records how the primary domain is served: TLSLetsEncrypt or
	// TLSNone. Empty on records written before this field existed, where callers
	// must fall back to inferring it from ProxyHost's scheme.
	//
	// Persisted because that inference is wrong after `ffm set-proxy --port 80`
	// rewrites ProxyHost to an http:// URL: a later re-render would then drop
	// the bench's TLS router.
	TLSMode string `json:"tls_mode,omitempty"`
	// Prod tuning as passed to create. Persisted so that regenerating
	// docker-compose.yml (recreate, or a domain change) reproduces the bench the
	// user asked for instead of silently resetting every knob to its default.
	// Zero/empty means "not recorded" and the create-time defaults apply.
	MariaDBBufferPool string `json:"mariadb_buffer_pool,omitempty"`
	GunicornWorkers   int    `json:"gunicorn_workers,omitempty"`
	WorkerLongCount   int    `json:"worker_long_count,omitempty"`
	WorkerShortCount  int    `json:"worker_short_count,omitempty"`
	RedisCacheMaxmem  string `json:"redis_cache_maxmem,omitempty"`
	RedisQueueMaxmem  string `json:"redis_queue_maxmem,omitempty"`
	SlowQueryLog      bool   `json:"slow_query_log,omitempty"`
	// MatchHostUser records that the image was built with the in-container
	// `frappe` user remapped onto the host user's uid/gid (--match-host-user).
	// Absent in records written before this option existed, which is correct —
	// those benches were built against the stock uid 1000.
	//
	// Persisted so that `restart --rebuild` and `recreate` re-render the same
	// Dockerfile; without it a rebuild would silently revert to uid 1000 and
	// break the bind mount on a host that needed the remap.
	MatchHostUser bool `json:"match_host_user,omitempty"`
	// Bind controls which host interfaces the bench's published ports listen
	// on: BindLoopback (127.0.0.1) or BindLAN (all interfaces). Empty on
	// records written before this field existed; see PublishHost for how those
	// are treated.
	Bind string `json:"bind,omitempty"`
	// SSHAgent records that the host SSH agent socket is forwarded into the
	// dev frappe container (--ssh-agent). Absent on older records, which then
	// stop forwarding the next time docker-compose.yml is rendered: forwarding
	// is opt-in because a bench created with it broke every later compose call
	// made without SSH_AUTH_SOCK (cron backups, sudo, the dashboard daemon).
	SSHAgent bool `json:"ssh_agent,omitempty"`
	// Agent marks the agent-ready profile (ffm create --agent, ffm agent on):
	// ffc and the MCP server use a dedicated System Manager user instead of
	// Administrator. AgentReadOnly limits the MCP server to read tools.
	Agent         bool `json:"agent,omitempty"`
	AgentReadOnly bool `json:"agent_read_only,omitempty"`
	// TemplateVersion is the bench.TemplateVersion this bench's
	// docker-compose.yml was last rendered from, by create or reconcile. Zero
	// on records written before it existed.
	TemplateVersion int `json:"template_version,omitempty"`
	// Tunnel holds the VPS tunnel configuration. Nil means no tunnel configured.
	Tunnel *TunnelState `json:"tunnel,omitempty"`
	// BackupSchedule is the scheduled-backup policy set by `ffm backup
	// schedule`. Nil means no scheduled backups.
	BackupSchedule *BackupPolicy `json:"backup_schedule,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
}

// Bind values for Bench.Bind.
const (
	BindLoopback = "loopback"
	BindLAN      = "lan"
)

// ErrNotFound is returned (wrapped) when no bench has the requested name.
var ErrNotFound = errors.New("bench not found")

// ErrNoBenches is returned when a command needs a bench and none exists.
var ErrNoBenches = errors.New("no benches found")

// PublishHost returns the host IP the bench's published ports bind to, or ""
// for all interfaces.
//
// Records without a Bind value predate the loopback default. Prod benches then
// bind to loopback: Traefik reaches the containers over the proxy network, and
// a host-side Caddy/nginx (the --no-ssl setup) reaches 127.0.0.1, so the only
// thing all-interfaces ever added was plain HTTP to gunicorn from the internet.
// Dev benches keep all interfaces, because users may rely on reaching them
// from another machine; `ffm reconcile --loopback` opts them in.
func (b Bench) PublishHost() string {
	switch b.Bind {
	case BindLAN:
		return ""
	case BindLoopback:
		return "127.0.0.1"
	}
	if b.IsProd() {
		return "127.0.0.1"
	}
	return ""
}

// IsProd reports whether the bench was created in production mode.
func (b Bench) IsProd() bool { return b.Mode == "prod" }

// IsDev reports whether the bench was created in development mode.
// Empty Mode is treated as dev for backward compatibility.
func (b Bench) IsDev() bool { return b.Mode != "prod" }

// DBEngine returns the effective database engine.
// Empty DBType is treated as "mariadb" for backward compatibility.
func (b Bench) DBEngine() string {
	if b.DBType == "postgres" {
		return "postgres"
	}
	return "mariadb"
}

// IsPostgres reports whether the bench uses PostgreSQL.
func (b Bench) IsPostgres() bool { return b.DBEngine() == "postgres" }

// Store is a thin wrapper around the benches.json state file.
// Writes are atomic (see Save), and Add, Remove and Update hold a lock file
// next to the state file across their read-modify-write, so concurrent
// processes cannot lose each other's updates. Save on its own is not locked.
type Store struct {
	path string
}

// Default returns a Store pointed at the standard state file.
func Default() *Store {
	return &Store{path: config.StateFile()}
}

// Load reads and returns all bench records. Returns an empty slice when the
// file does not yet exist.
func (s *Store) Load() ([]Bench, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Bench{}, nil
	}
	if err != nil {
		return nil, err
	}
	var benches []Bench
	if err := json.Unmarshal(data, &benches); err != nil {
		return nil, err
	}
	return benches, nil
}

// Save persists the full bench slice, replacing any existing file.
//
// The write is atomic: a temp file in the same directory is written, synced
// and renamed over the state file. A reader in another process (the hourly
// `ffm backup run-due`, the dashboard) therefore sees either the old file or
// the new one, never a truncated half-write.
//
// The file is 0600 because every record carries the bench's Administrator and
// database root passwords. Saving also tightens a file an older ffm left 0644.
func (s *Store) Save(benches []Bench) error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(benches, "", "  ")
	if err != nil {
		return err
	}
	// Keep the previous version: one bad write (or a bug in a caller) would
	// otherwise lose every bench's record, passwords included, with no way
	// back. The copy is as private as the file.
	if prev, err := os.ReadFile(s.path); err == nil && !bytes.Equal(prev, data) {
		if err := writeFileAtomic(s.path+".bak", prev, 0o600); err != nil {
			return fmt.Errorf("back up %s: %w", filepath.Base(s.path), err)
		}
	}
	return writeFileAtomic(s.path, data, 0o600)
}

// writeFileAtomic writes data to path through a synced temp file and a rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	keepOwner(tmp, path)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// stateLockTimeout bounds how long a writer waits for another process's
// read-modify-write of benches.json. Each one takes milliseconds.
const stateLockTimeout = 10 * time.Second

// locked runs fn while holding an exclusive lock next to the state file, so
// read-modify-write sequences from different processes (CLI commands, the
// dashboard daemon, the hourly backup job) cannot lose each other's updates.
// Writes were already atomic; this makes them serialised.
func (s *Store) locked(fn func() error) error {
	l, err := lock.Acquire(s.path+".lock", stateLockTimeout)
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(s.path), err)
	}
	defer l.Release()
	return fn()
}

// Add appends a new bench record and saves.
func (s *Store) Add(b Bench) error {
	return s.locked(func() error { return s.add(b) })
}

func (s *Store) add(b Bench) error {
	benches, err := s.Load()
	if err != nil {
		return err
	}
	benches = append(benches, b)
	return s.Save(benches)
}

// Remove deletes the bench with the given name and saves.
func (s *Store) Remove(name string) error {
	return s.locked(func() error { return s.remove(name) })
}

func (s *Store) remove(name string) error {
	benches, err := s.Load()
	if err != nil {
		return err
	}
	filtered := benches[:0]
	for _, b := range benches {
		if b.Name != name {
			filtered = append(filtered, b)
		}
	}
	return s.Save(filtered)
}

// Get returns the bench record for the given name, or an error if not found.
func (s *Store) Get(name string) (Bench, error) {
	benches, err := s.Load()
	if err != nil {
		return Bench{}, err
	}
	for _, b := range benches {
		if b.Name == name {
			return b, nil
		}
	}
	return Bench{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Exists reports whether a bench with the given name is tracked.
func (s *Store) Exists(name string) (bool, error) {
	benches, err := s.Load()
	if err != nil {
		return false, err
	}
	for _, b := range benches {
		if b.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Update applies fn to the bench with the given name and saves.
func (s *Store) Update(name string, fn func(*Bench)) error {
	return s.locked(func() error { return s.update(name, fn) })
}

func (s *Store) update(name string, fn func(*Bench)) error {
	benches, err := s.Load()
	if err != nil {
		return err
	}
	for i := range benches {
		if benches[i].Name == name {
			fn(&benches[i])
			return s.Save(benches)
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, name)
}

// UsedPorts returns the set of web and socketio ports already assigned.
func (s *Store) UsedPorts() (map[int]bool, error) {
	benches, err := s.Load()
	if err != nil {
		return nil, err
	}
	used := make(map[int]bool, len(benches)*2)
	for _, b := range benches {
		used[b.WebPort] = true
		used[b.SocketIOPort] = true
	}
	return used, nil
}
