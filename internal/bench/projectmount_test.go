package bench

import (
	"strings"
	"testing"
)

func TestProjectMountRendering(t *testing.T) {
	base := ComposeData{Name: "demo", Mode: "dev", DBType: "mariadb", WebPort: 8000, WebPortEnd: 8005, SocketIOPort: 9000, SocketIOPortEnd: 9005}

	off, err := RenderCompose(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(off), "create_host_path") || strings.Contains(string(off), "/apps/") {
		t.Errorf("a mount rendered without a project app:\n%s", off)
	}

	on := base
	on.ProjectApp, on.ProjectAppPath = "my_app", "/home/dev/src/my app"
	got, err := RenderCompose(on)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- type: bind",
		`source: "/home/dev/src/my app"`,
		"target: /workspace/frappe-bench/apps/my_app",
		"create_host_path: false",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if i, j := strings.Index(string(got), "./workspace:/workspace"), strings.Index(string(got), "type: bind"); i < 0 || j < i {
		t.Errorf("the checkout must be mounted after ./workspace (%d, %d)", i, j)
	}
}

func TestProjectMountNeverRenderedInProd(t *testing.T) {
	d := ComposeData{Name: "demo", Mode: "prod", DBType: "mariadb", Domain: "erp.example.com", SiteName: "erp.example.com",
		WebPort: 8000, SocketIOPort: 9000, GunicornWorkers: 2, WorkerLongCount: 1, WorkerShortCount: 1,
		MariaDBBufferPool: "1G", RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb",
		ProjectApp: "my_app", ProjectAppPath: "/home/dev/src/my_app"}
	out, err := RenderCompose(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "my_app") || strings.Contains(string(out), "create_host_path") {
		t.Errorf("prod compose carries the project mount:\n%s", out)
	}
}

func TestValidateProjectMount(t *testing.T) {
	for _, tt := range []struct {
		app, path string
		ok        bool
	}{
		{"my_app", "/home/dev/my_app", true},
		{"my_app", "/home/dev/my app", true},
		{"my_app", "relative/path", false},
		{"my_app", "/home/$USER/app", false},
		{"my_app", `/home/"x"/app`, false},
		{"my_app", `/home/a\b`, false},
		{"my_app", "/home/a\nb", false},
		{"My-App", "/home/dev/app", false},
		{"frappe", "/home/dev/app", false},
		{"", "/home/dev/app", false},
	} {
		if err := ValidateProjectMount(tt.app, tt.path); (err == nil) != tt.ok {
			t.Errorf("ValidateProjectMount(%q, %q) = %v, want ok=%v", tt.app, tt.path, err, tt.ok)
		}
	}
	// RenderCompose refuses what the validator refuses, so no caller can skip it.
	_, err := RenderCompose(ComposeData{Mode: "dev", DBType: "mariadb", ProjectApp: "a", ProjectAppPath: "/x/$y"})
	if err == nil {
		t.Error("RenderCompose accepted a path with $")
	}
}
