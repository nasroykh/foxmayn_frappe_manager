package bench

import (
	"slices"
	"strings"
	"testing"
)

func TestDevComposeRunsMailpitOnWebPortPlusSix(t *testing.T) {
	data := ComposeData{Name: "a", Mode: "dev", DBType: "mariadb", PublishHost: "127.0.0.1",
		WebPort: 8010, WebPortEnd: 8015, SocketIOPort: 9010, SocketIOPortEnd: 9015}
	out, err := RenderCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "image: docker.io/axllent/mailpit:v1.31.4") {
		t.Error("dev compose has no pinned mailpit service")
	}
	if !strings.Contains(s, `"127.0.0.1:8016:8025"`) {
		t.Errorf("mailpit UI not published on 127.0.0.1:8016:\n%s", s)
	}
	if strings.Contains(s, ":1025\"") {
		t.Error("mailpit SMTP is published on the host; it must stay on the bench network")
	}

	data.Mode = "prod"
	data.Domain = "erp.example.com"
	out, err = RenderCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "mailpit") {
		t.Error("prod compose runs mailpit")
	}
}

func TestBenchPortRangeIncludesMailPort(t *testing.T) {
	ports := BenchPortRange(8000, 9000)
	if !slices.Contains(ports, 8006) || len(ports) != 13 {
		t.Errorf("BenchPortRange = %v, want the 12 published ports plus 8006", ports)
	}
}
