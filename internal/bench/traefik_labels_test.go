package bench

import (
	"strings"
	"testing"
)

func TestProdTraefikLabels(t *testing.T) {
	d := ComposeData{Name: "p", Mode: "prod", DBType: "mariadb", Domain: "erp.example.com", SiteName: "erp.example.com",
		WebPort: 8000, SocketIOPort: 9000, GunicornWorkers: 2, WorkerLongCount: 1, WorkerShortCount: 1,
		MariaDBBufferPool: "1G", RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb"}
	out, err := RenderCompose(d)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), `- "traefik.`) {
			labels = append(labels, l)
		}
	}
	s := strings.Join(labels, "\n")
	for _, want := range []string{
		"traefik.http.routers.p.middlewares=p-headers,p-compress",
		"p-headers.headers.stsSeconds=31536000",
		"p-headers.headers.contentTypeNosniff=true",
		"p-compress.compress=true",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("TLS compose lacks %q", want)
		}
	}
	if strings.Contains(s, "includeSubDomains") || strings.Contains(s, "customRequestHeaders.Origin") {
		t.Error("HSTS must not cover subdomains, and socket.io must get the browser's Origin")
	}
	d.DomainAliases = []string{"erp.internal"}
	out, _ = RenderCompose(d)
	if strings.Contains(string(out), "customRequestHeaders.Origin") || !strings.Contains(string(out), "X-Frappe-Site-Name=erp.example.com") {
		t.Error("alias socket.io: Origin must pass through and the site name be set")
	}
	d.DomainAliases = nil
	d.NoSSL = true
	out, _ = RenderCompose(d)
	if strings.Contains(string(out), "stsSeconds") {
		t.Error("HSTS on a plain-HTTP bench")
	}
}
