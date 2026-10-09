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
