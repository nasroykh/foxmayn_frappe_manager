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

// ValidBenchPortPair reports whether webPort and socketIOPort match the
// pairing used by ffm (socketIOBase - webBase offset between the two).
func ValidBenchPortPair(webPort, socketIOPort int) bool {
	return socketIOPort == webPort+(socketIOBase-webBase)
}

// CheckTCPPortsFree returns an error if any listed TCP port cannot be bound
// on the host (same check used during AllocatePorts).
func CheckTCPPortsFree(ports ...int) error {
	for _, p := range ports {
		if err := probePort(p); err != nil {
			return fmt.Errorf("port %d: %w", p, err)
		}
	}
	return nil
}

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

// PublishedPortSpan is how many consecutive host ports a bench publishes from
// each base. The dev compose template maps "<web>-<web+5>:8000-8005" and the
// same for Socket.IO, so a bench occupies six ports per base, not one.
const PublishedPortSpan = 6

// BenchPortRange returns every host port a bench publishes for a port pair.
func BenchPortRange(webPort, socketIOPort int) []int {
	ports := make([]int, 0, PublishedPortSpan*2)
	for i := 0; i < PublishedPortSpan; i++ {
		ports = append(ports, webPort+i, socketIOPort+i)
	}
	return ports
}

// CheckBenchPortRangeFree probes every port a bench would publish, not just the
// two base ports.
//
// CheckTCPPortsFree only checks the ports it is handed, and every caller hands
// it the bases — so a port pair can pass validation and then fail at
// `docker compose up` with a bind error on, say, 8003. Restore reuses a
// recorded pair, which makes it the most likely command to hit exactly that.
func CheckBenchPortRangeFree(webPort, socketIOPort int) error {
	return CheckTCPPortsFree(BenchPortRange(webPort, socketIOPort)...)
}
