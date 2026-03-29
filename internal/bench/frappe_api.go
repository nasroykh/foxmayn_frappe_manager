package bench

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIKeys holds a Frappe API key/secret pair.
type APIKeys struct {
	Key    string
	Secret string
}

// GenerateAdminAPIKeys generates an API key/secret for the Administrator user
// using the documented bench execute command. bench execute returns a Python
// dict, so we pipe through ast.literal_eval to convert it to JSON.
func (r *Runner) GenerateAdminAPIKeys(siteName string) (APIKeys, error) {
	cmd := fmt.Sprintf(
		`cd /workspace/frappe-bench && bench --site %s execute frappe.core.doctype.user.user.generate_keys --args "['Administrator']" 2>/dev/null | python3 -c "import sys,json,ast; print(json.dumps(ast.literal_eval(sys.stdin.read().strip())))"`,
		siteName,
	)
	out, err := r.ExecSilent("frappe", "bash", "-c", cmd)
	if err != nil {
		return APIKeys{}, fmt.Errorf("bench execute generate_keys: %w (output: %s)", err, out)
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var parsed struct {
			APIKey    string `json:"api_key"`
			APISecret string `json:"api_secret"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err == nil && parsed.APIKey != "" {
			return APIKeys{Key: parsed.APIKey, Secret: parsed.APISecret}, nil
		}
	}
	return APIKeys{}, fmt.Errorf("could not parse API keys from output: %s", out)
}
