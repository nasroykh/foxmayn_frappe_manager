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
