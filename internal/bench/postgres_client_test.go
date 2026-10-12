package bench

import (
	"regexp"
	"strings"
	"testing"
)

// pg_dump refuses a server newer than itself, and frappe/bench ships an older
// client than docker.io/postgres:<major>. The image tag and the client the
// Dockerfiles install must therefore carry the same major, in both modes.
func TestPostgresClientMatchesServerImage(t *testing.T) {
	image := regexp.MustCompile(`docker\.io/postgres:(\S+)`)
	client := regexp.MustCompile(`postgresql-client-(\d+)`)
	for _, mode := range []string{"dev", "prod"} {
		d := ComposeData{Mode: mode, Name: "x", Domain: "x.example.com", DBType: "postgres"}
		compose := renderFor(t, d)
		m := image.FindStringSubmatch(compose)
		if m == nil {
			t.Fatalf("%s: compose has no postgres image", mode)
		}
		df, err := RenderDockerfile(d)
		if err != nil {
			t.Fatal(err)
		}
		c := client.FindStringSubmatch(string(df))
		if c == nil {
			t.Fatalf("%s: Dockerfile installs no postgresql-client", mode)
		}
		if m[1] != PostgresMajor || c[1] != PostgresMajor {
			t.Errorf("%s: server image postgres:%s, client %s, PostgresMajor %s; all three must agree", mode, m[1], c[1], PostgresMajor)
		}
		for _, want := range []string{"apt.postgresql.org.sh", "pg_dump --version | grep -q ' " + PostgresMajor + `\.'`} {
			if !strings.Contains(string(df), want) {
				t.Errorf("%s: Dockerfile lacks %q", mode, want)
			}
		}
	}
}

// A literal tag in a compose template would let the server move without the
// client.
func TestComposeTemplatesTakePostgresMajorFromConstant(t *testing.T) {
	lit := regexp.MustCompile(`postgres:\d`)
	for name, tmpl := range map[string]string{"dev": devComposeTmpl, "prod": prodComposeTmpl} {
		if lit.MatchString(tmpl) {
			t.Errorf("%s compose template hardcodes a postgres tag; use {{.PostgresMajor}}", name)
		}
	}
}

// MariaDB benches do not pay for the PGDG repo or the client.
func TestMariaDBDockerfileHasNoPostgresClient(t *testing.T) {
	for _, mode := range []string{"dev", "prod"} {
		df, err := RenderDockerfile(ComposeData{Mode: mode, DBType: "mariadb"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(df), "postgres") {
			t.Errorf("%s: mariadb Dockerfile mentions postgres", mode)
		}
	}
}
