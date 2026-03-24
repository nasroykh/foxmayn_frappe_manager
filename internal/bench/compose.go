package bench

import (
	_ "embed"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/docker-compose.yml.tmpl
var composeTmpl string

// ComposeData holds the values substituted into the compose template.
type ComposeData struct {
	BenchDir            string
	WebPort             int
	WebPortEnd          int
	SocketIOPort        int
	SocketIOPortEnd     int
	MariaDBRootPassword string
}

// WriteCompose renders the compose template into the bench directory.
func WriteCompose(benchDir string, data ComposeData) error {
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}

	tmpl, err := template.New("compose").Parse(composeTmpl)
	if err != nil {
		return err
	}

	dest := filepath.Join(benchDir, "docker-compose.yml")
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}
