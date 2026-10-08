package bench

import (
	"strings"
	"testing"
)

func TestToolchainFor(t *testing.T) {
	for _, tc := range []struct {
		branch       string
		python, node string
	}{
		{"version-15", "3.12", "22"},
		{"version-15-hotfix", "3.12", "22"},
		{"version-16", "3.14", "24"},
		{"develop", "3.14", "24"},
		{"main", "3.14", "24"},
		{"", "3.14", "24"},
	} {
		got := ToolchainFor(tc.branch)
		if got.Python != tc.python || got.Node != tc.node {
			t.Errorf("ToolchainFor(%q) = %+v, want Python %s Node %s", tc.branch, got, tc.python, tc.node)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("ToolchainFor(%q) does not validate: %v", tc.branch, err)
		}
	}
}

func TestToolchainValidateRefusesWhatTheImageLacks(t *testing.T) {
	for _, tc := range []Toolchain{
		{Python: "3.11", Node: "22"},
		{Python: "3.14", Node: "18"},
		{Python: "3.14; rm -rf /", Node: "24"},
		{Python: "", Node: "24"},
	} {
		if err := tc.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want an error", tc)
		}
	}
}

func TestToolchainValidateForVersion16(t *testing.T) {
	if err := (Toolchain{Python: "3.12", Node: "24"}).ValidateFor("version-16"); err == nil {
		t.Error("Python 3.12 accepted for version-16")
	}
	if err := (Toolchain{Python: "3.14", Node: "22"}).ValidateFor("version-16"); err == nil {
		t.Error("Node 22 accepted for version-16")
	}
	if err := (Toolchain{Python: "3.14", Node: "22"}).ValidateFor("version-15"); err != nil {
		t.Errorf("version-15 on 3.14/22 refused: %v", err)
	}
}

func TestDockerfileSwitchesNodeOnlyWhenRecorded(t *testing.T) {
	for _, mode := range []string{"dev", "prod"} {
		out, err := RenderDockerfile(ComposeData{Mode: mode, DBType: "mariadb", NodeMajor: "22"})
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		if !strings.Contains(s, "nvm alias default 22") || !strings.Contains(s, "grep -q '^v22\\.'") {
			t.Errorf("%s Dockerfile with NodeMajor 22 does not switch Node:\n%s", mode, s)
		}
		if strings.Index(s, "nvm use 22") > strings.Index(s, "corepack enable pnpm") {
			t.Errorf("%s Dockerfile enables corepack before switching Node; pnpm would miss the new node", mode)
		}

		out, err = RenderDockerfile(ComposeData{Mode: mode, DBType: "mariadb"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "nvm") {
			t.Errorf("%s Dockerfile without NodeMajor touches nvm; old benches must keep the image default", mode)
		}
	}
}

// The uv cache must live inside the pip-cache volume, whose mount point exists
// in the image and is owned by frappe. A volume of its own at ~/.cache/uv is
// created root-owned and every uv call fails with "Permission denied".
func TestDevComposeHasUVCache(t *testing.T) {
	out, err := RenderCompose(ComposeData{Name: "a", Mode: "dev", DBType: "mariadb", WebPort: 8000, WebPortEnd: 8005, SocketIOPort: 9000, SocketIOPortEnd: 9005})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"uv-cache", "yarn-cache"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("dev compose mounts a %s volume; its root would be owned by root", bad)
		}
	}
	for _, want := range []string{"pip-cache:/home/frappe/.cache/pip", "UV_CACHE_DIR=/home/frappe/.cache/pip/uv",
		"YARN_CACHE_FOLDER=/home/frappe/.cache/pip/yarn", "UV_LINK_MODE=copy"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("dev compose lacks %q", want)
		}
	}
}
