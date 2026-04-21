package state

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Bench holds the persisted state for a single managed bench.
type Bench struct {
	Name          string `json:"name"`
	Dir           string `json:"dir"`
	WebPort       int    `json:"web_port"`
	SocketIOPort  int    `json:"socketio_port"`
	FrappeBranch  string `json:"frappe_branch"`
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
	Domain    string    `json:"domain,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
// It is not concurrency-safe across processes; we rely on short-lived CLI
// invocations and don't need a full lock file for v0.1.
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
func (s *Store) Save(benches []Bench) error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(benches, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Add appends a new bench record and saves.
func (s *Store) Add(b Bench) error {
	benches, err := s.Load()
	if err != nil {
		return err
	}
	benches = append(benches, b)
	return s.Save(benches)
}

// Remove deletes the bench with the given name and saves.
func (s *Store) Remove(name string) error {
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
	return Bench{}, errors.New("bench not found: " + name)
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
	return errors.New("bench not found: " + name)
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
