package manager

import (
	"sync"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Service performs bench lifecycle operations shared by the CLI and dashboard.
type Service struct {
	Store   *state.Store
	Verbose bool
	mu      sync.Mutex
}

// Default returns a Service using the standard state file.
func Default() *Service {
	return New(false)
}

// New returns a Service with the given verbose flag for docker compose output.
func New(verbose bool) *Service {
	return &Service{
		Store:   state.Default(),
		Verbose: verbose,
	}
}

func (s *Service) lock()   { s.mu.Lock() }
func (s *Service) unlock() { s.mu.Unlock() }

// LoadBenches returns all bench records under the state lock.
func (s *Service) LoadBenches() ([]state.Bench, error) {
	s.lock()
	defer s.unlock()
	return s.Store.Load()
}

// GetBench returns one bench by name.
func (s *Service) GetBench(name string) (state.Bench, error) {
	s.lock()
	defer s.unlock()
	return s.Store.Get(name)
}

// AddBench appends a bench record.
func (s *Service) AddBench(b state.Bench) error {
	s.lock()
	defer s.unlock()
	return s.Store.Add(b)
}

// RemoveBench deletes a bench from state.
func (s *Service) RemoveBench(name string) error {
	s.lock()
	defer s.unlock()
	return s.Store.Remove(name)
}

// UpdateBench mutates a bench in place.
func (s *Service) UpdateBench(name string, fn func(*state.Bench)) error {
	s.lock()
	defer s.unlock()
	return s.Store.Update(name, fn)
}

// BenchExists reports whether a bench name is registered.
func (s *Service) BenchExists(name string) (bool, error) {
	s.lock()
	defer s.unlock()
	return s.Store.Exists(name)
}
