package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// claudeMcpFile matches Claude Code project config and foxmayn-frappe-mcp's
// "Manual Configuration" for Claude (.mcp.json).
// See: https://github.com/nasroykh/foxmayn_frappe_mcp
type claudeMcpFile struct {
	MCPServers map[string]claudeMcpServerEntry `json:"mcpServers"`
}

type claudeMcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func claudeMcpConfigBytes(benchName string) ([]byte, error) {
	cfg := claudeMcpFile{
		MCPServers: map[string]claudeMcpServerEntry{
			"frappe": {
				Command: "ffc",
				Args:    []string{"mcp", "--site", benchName},
			},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func writeClaudeMcpConfigHost(frappeBenchDir, benchName string) error {
	if err := os.MkdirAll(frappeBenchDir, 0o755); err != nil {
		return fmt.Errorf("mkdir frappe-bench: %w", err)
	}
	payload, err := claudeMcpConfigBytes(benchName)
	if err != nil {
		return fmt.Errorf("marshal .mcp.json: %w", err)
	}
	path := filepath.Join(frappeBenchDir, ".mcp.json")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func ensureClaudeMcpConfigHost(frappeBenchDir, benchName string) error {
	path := filepath.Join(frappeBenchDir, ".mcp.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat .mcp.json: %w", err)
	}
	return writeClaudeMcpConfigHost(frappeBenchDir, benchName)
}
