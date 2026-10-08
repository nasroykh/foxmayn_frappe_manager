package bench

import "testing"

// templatesAtVersion maps each TemplateVersion to the fingerprint of the
// templates it names. When a template changes, this test fails: bump
// TemplateVersion and add the new fingerprint below.
var templatesAtVersion = map[int]string{
	1: "5ecf0c6653195aa260ea8545fddc1a09cd17b333709af53bf479b226ad10bf86",
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
