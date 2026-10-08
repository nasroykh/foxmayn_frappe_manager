package bench

import "testing"

func TestValidateNewNameRejectsReserved(t *testing.T) {
	for _, name := range []string{"list", "schedule", "prune", "run-due", "scheduler", "List"} {
		if err := ValidateNewName(name); err == nil {
			t.Errorf("ValidateNewName(%q) accepted a reserved name", name)
		}
		if name != "run-due" {
			if err := ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) must still accept existing benches: %v", name, err)
			}
		}
	}
	if err := ValidateNewName("listing"); err != nil {
		t.Errorf("ValidateNewName(listing) = %v", err)
	}
}

func TestValidateNewNameRejectsUppercase(t *testing.T) {
	if err := ValidateNewName("MyBench"); err == nil {
		t.Fatal("uppercase name accepted; docker compose rejects the project name ffm-MyBench")
	}
	if err := ValidateNewName("my-bench2"); err != nil {
		t.Fatalf("lowercase name rejected: %v", err)
	}
}
