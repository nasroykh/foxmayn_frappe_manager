package manager

import (
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// A stopped bench still owns its ports. Probing alone cannot see that, so a
// restore that reused the archive's pair would leave two benches recorded on
// one range and break `ffm start` on the bench it never touched.
func TestRestorePortsWillNotStealAStoppedBenchsPorts(t *testing.T) {
	// A high pair, so the live probe passes and the state store is what decides.
	const web, sio = 8540, 9540

	newService := func(t *testing.T, tracked []state.Bench) *Service {
		t.Helper()
		t.Setenv("FFM_CONFIG_DIR", t.TempDir())
		s := New(false)
		for _, b := range tracked {
			if err := s.AddBench(b); err != nil {
				t.Fatalf("seed state: %v", err)
			}
		}
		return s
	}

	m := devManifest()
	m.Bench.WebPort = web
	m.Bench.SocketIOPort = sio

	t.Run("reuses the pair when nothing owns it", func(t *testing.T) {
		s := newService(t, nil)
		gotWeb, gotSio, err := s.restorePorts(m, RestoreInput{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotWeb != web || gotSio != sio {
			t.Fatalf("ports = %d/%d, want %d/%d — a free pair should be reused so URLs stay stable",
				gotWeb, gotSio, web, sio)
		}
	})

	t.Run("falls back to allocation when a tracked bench owns it", func(t *testing.T) {
		s := newService(t, []state.Bench{{Name: "src", WebPort: web, SocketIOPort: sio}})
		gotWeb, gotSio, err := s.restorePorts(m, RestoreInput{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotWeb != 0 || gotSio != 0 {
			t.Fatalf("ports = %d/%d, want 0/0 (fresh allocation) — those belong to bench %q",
				gotWeb, gotSio, "src")
		}
	})

	t.Run("a bench overlapping only the published range also blocks reuse", func(t *testing.T) {
		// A bench on 8535 publishes 8535-8540, so it collides with 8540 even
		// though the two base ports differ. CheckTCPPortsFree, which probes only
		// the bases, cannot see this.
		s := newService(t, []state.Bench{{Name: "src", WebPort: web - 5, SocketIOPort: sio - 5}})
		gotWeb, _, err := s.restorePorts(m, RestoreInput{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotWeb != 0 {
			t.Fatalf("ports = %d, want 0: 8535-8540 overlaps the requested range", gotWeb)
		}
	})

	t.Run("--reallocate-ports always allocates", func(t *testing.T) {
		s := newService(t, nil)
		gotWeb, gotSio, err := s.restorePorts(m, RestoreInput{ReallocatePorts: true})
		if err != nil || gotWeb != 0 || gotSio != 0 {
			t.Fatalf("ports = %d/%d err=%v, want 0/0", gotWeb, gotSio, err)
		}
	})
}
