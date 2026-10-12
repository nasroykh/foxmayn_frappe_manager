package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrUnknownService means the named service is not one of the bench's compose
// services. Callers map it to a 400-class answer.
var ErrUnknownService = errors.New("unknown service")

type composeServicesEntry struct {
	modTime  time.Time
	size     int64
	services []string
}

var (
	composeServicesMu    sync.Mutex
	composeServicesCache = map[string]composeServicesEntry{}
)

// ComposeServices returns the services defined by the bench's compose files,
// from `docker compose config --services`. The answer is cached per bench and
// stays valid until docker-compose.yml (or the user's override file) changes,
// which is exactly when a reconcile, domain change or recreate rewrites it.
func (s *Service) ComposeServices(name string) ([]string, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return nil, err
	}
	var modTime time.Time
	var size int64
	for _, f := range []string{"docker-compose.yml", "docker-compose.override.yml"} {
		if fi, err := os.Stat(filepath.Join(b.Dir, f)); err == nil {
			if fi.ModTime().After(modTime) {
				modTime = fi.ModTime()
			}
			size += fi.Size()
		}
	}
	key := b.Dir
	composeServicesMu.Lock()
	e, ok := composeServicesCache[key]
	composeServicesMu.Unlock()
	if ok && e.modTime.Equal(modTime) && e.size == size {
		return append([]string(nil), e.services...), nil
	}
	services, err := s.quietRunnerFor(b).ConfigServices()
	if err != nil {
		return nil, err
	}
	composeServicesMu.Lock()
	composeServicesCache[key] = composeServicesEntry{modTime: modTime, size: size, services: services}
	composeServicesMu.Unlock()
	return append([]string(nil), services...), nil
}

// ValidateService returns ErrUnknownService (wrapped) unless service is one of
// the bench's compose services.
func (s *Service) ValidateService(name, service string) error {
	services, err := s.ComposeServices(name)
	if err != nil {
		return err
	}
	for _, sv := range services {
		if sv == service {
			return nil
		}
	}
	return fmt.Errorf("%w %q (bench %s has: %v)", ErrUnknownService, service, name, services)
}
