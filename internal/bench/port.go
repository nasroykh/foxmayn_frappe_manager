package bench

import (
	"fmt"
	"net"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

const (
	webBase      = 8000
	socketIOBase = 9000
	portStep     = 10
	maxBenches   = 50
)

// AllocatePorts returns the next available (webPort, socketIOPort) pair.
// It scans the state store to avoid already-assigned ports, then probes the
// host to detect external conflicts.
func AllocatePorts(store *state.Store) (webPort, socketIOPort int, err error) {
	used, err := store.UsedPorts()
	if err != nil {
		return 0, 0, err
	}

	for i := 0; i < maxBenches; i++ {
		wp := webBase + i*portStep
		sp := socketIOBase + i*portStep
		if used[wp] || used[sp] {
			continue
		}
		if err := probePort(wp); err != nil {
			continue
		}
		if err := probePort(sp); err != nil {
			continue
		}
		return wp, sp, nil
	}
	return 0, 0, fmt.Errorf("no free port pair found in range %d-%d", webBase, webBase+maxBenches*portStep)
}

// probePort tries to bind the port to verify it is free on the host.
func probePort(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	ln.Close()
	return nil
}
