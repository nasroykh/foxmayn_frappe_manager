package bench

import (
	"strings"
	"testing"
)

func prodCompose(t *testing.T, d ComposeData) string {
	t.Helper()
	d.Name, d.Mode, d.Domain, d.SiteName = "p", "prod", "erp.example.com", "erp.example.com"
	d.WebPort, d.SocketIOPort, d.GunicornWorkers, d.WorkerLongCount, d.WorkerShortCount = 8000, 9000, 2, 1, 1
	d.MariaDBBufferPool, d.RedisCacheMaxmem, d.RedisQueueMaxmem = "1G", "512mb", "512mb"
	if d.DBType == "" {
		d.DBType = "mariadb"
	}
	out, err := RenderCompose(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestProdComposeHardening(t *testing.T) {
	s := prodCompose(t, ComposeData{})
	for _, want := range []string{
		"no-new-privileges:true",
		"--innodb-flush-log-at-trx-commit=1",
		"redis-queue-data:/data", `"--appendonly", "yes"`, "\n  redis-queue-data:\n",
		"--max-requests 5000 --max-requests-jitter 500", "--worker-tmp-dir /dev/shm", "--graceful-timeout 30",
		"http://127.0.0.1:8000/api/method/ping", "/dev/tcp/127.0.0.1/9000", `"redis-cli", "ping"`,
		"condition: service_healthy",
		"YARN_CACHE_FOLDER=/home/frappe/.cache/pip/yarn",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("prod compose lacks %q", want)
		}
	}
	if strings.Contains(s, "yarn-cache") {
		t.Error("prod compose still mounts the root-owned yarn-cache volume")
	}
	// Every service gets no-new-privileges: 2 Redis, DB, frappe, socketio,
	// two workers, scheduler.
	if n := strings.Count(s, "security_opt: *default-security"); n != 8 {
		t.Errorf("security_opt on %d services, want 8", n)
	}
	if s := prodCompose(t, ComposeData{MariaDBFastCommit: true}); !strings.Contains(s, "--innodb-flush-log-at-trx-commit=2") {
		t.Error("--mariadb-fast-commit not honoured")
	}
	if s := prodCompose(t, ComposeData{DBType: "postgres"}); strings.Count(s, "security_opt: *default-security") != 8 {
		t.Error("postgres compose lacks no-new-privileges somewhere")
	}
}
