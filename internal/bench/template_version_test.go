package bench

import "testing"

// templatesAtVersion maps each TemplateVersion to the fingerprint of the
// templates it names. When a template changes, this test fails: bump
// TemplateVersion and add the new fingerprint below.
var templatesAtVersion = map[int]string{
	1: "5ecf0c6653195aa260ea8545fddc1a09cd17b333709af53bf479b226ad10bf86",
	// 2: per-branch Node in both Dockerfiles; dev compose: uv cache inside
	// pip-cache, Mailpit, ./home bind mounts for Claude Code and ffc.
	2: "a61f677ea32547909056f936178874814090d3ba0375efd88bd81947ad6fafd7",
	// 3: dev compose: yarn cache inside pip-cache, yarn-cache volume dropped.
	3: "be5eb6ecd3118ca9c28b5b499f4ae7614dfbb39605677d8da012660954ad8db9",
	// 4: prod hardening (healthchecks, no-new-privileges, redis-queue volume,
	// gunicorn flags, durable MariaDB commits, yarn cache in pip-cache);
	// Traefik headers/compress labels, socketio Origin no longer pinned.
	4: "9a4b3c9dcca5c054216d23cb1b1327485c70b33664f7b7c3924f3aef9208b7e1",
	// 5: dev compose: optional bind mount of a project checkout over
	// apps/<app> (ffm up in an app repository).
	5: "c09db983a04ca5383a2e18b49165998de5274944d6b0b6035bee0e65deeaec16",
	// 6: dev Dockerfile: ffc pinned to bench.FfcRef (binary and skills).
	6: "3471151b74a4fc5e6eacae93627bd83859c000cf282b49d18c8fdc582f89568f",
	// 7: compose: the database root password is a file secret
	// (./secrets/db_root_password), not an inline environment variable.
	7: "08d110b360d18fff78807fb64c3173f58971a1395b108883f0413f168840dbfb",
	// 8: postgres benches: both Dockerfiles install the PGDG PostgreSQL client
	// of bench.PostgresMajor; compose image tag comes from the same constant.
	8: "a72e5b2928e1166c84da7b78a4b9530a62a3e6d914e194d2fcc9892d24ce53f1",
	// 9: both Dockerfiles: the base image is pinned by digest (bench.BenchImage).
	9: "0e77ee74e1efaf3daa90c000595f39a9cd403ad65ef80df50ea6bae8a78d6ae6",
	// 10: dev compose: socket.io routed through Traefik on the bench hosts
	// (<name>-socketio router, X-Frappe-Site-Name header).
	10: "a2dcb4d017c61e8faeb5ea78a9bc2eec75602a7943c999bbc960119de8557ac9",
}

func TestTemplateVersionTracksTemplates(t *testing.T) {
	want, ok := templatesAtVersion[TemplateVersion]
	if !ok {
		t.Fatalf("TemplateVersion %d has no recorded fingerprint; add %q", TemplateVersion, templatesFingerprint())
	}
	if got := templatesFingerprint(); got != want {
		t.Fatalf("the templates changed but TemplateVersion is still %d: bump it and record %q", TemplateVersion, got)
	}
}
