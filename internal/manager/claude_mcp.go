package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// claudeMcpFile is Claude Code's project config (.mcp.json). This file wires
// Claude Code inside the container to ffc's MCP server (`ffc mcp --site
// <bench>`); the host's AI clients are set up with `ffm ffc --host`.
type claudeMcpFile struct {
	MCPServers map[string]claudeMcpServerEntry `json:"mcpServers"`
}

type claudeMcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func claudeMcpConfigBytes(benchName string, readOnly bool) ([]byte, error) {
	args := []string{"mcp", "--site", benchName}
	if readOnly {
		args = append(args, "--read-only")
	}
	cfg := claudeMcpFile{
		MCPServers: map[string]claudeMcpServerEntry{
			"frappe": {Command: "ffc", Args: args},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func writeClaudeMcpConfigHost(frappeBenchDir, benchName string, readOnly bool) error {
	if err := os.MkdirAll(frappeBenchDir, 0o755); err != nil {
		return fmt.Errorf("mkdir frappe-bench: %w", err)
	}
	payload, err := claudeMcpConfigBytes(benchName, readOnly)
	if err != nil {
		return fmt.Errorf("marshal .mcp.json: %w", err)
	}
	path := filepath.Join(frappeBenchDir, ".mcp.json")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func ensureClaudeMcpConfigHost(frappeBenchDir, benchName string, readOnly bool) error {
	path := filepath.Join(frappeBenchDir, ".mcp.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat .mcp.json: %w", err)
	}
	return writeClaudeMcpConfigHost(frappeBenchDir, benchName, readOnly)
}
