package bench

import (
	"fmt"
	"slices"
	"strings"
)

// DefaultFrappeBranch is the Frappe branch a new bench uses when none is given.
const DefaultFrappeBranch = "version-16"

// Toolchain is the Python and Node a bench runs on, as "major.minor" and
// "major" (e.g. 3.12 and 22).
type Toolchain struct {
	Python string
	Node   string
}

// The toolchains the pinned frappe/bench image (BenchImageTag) ships: pyenv
// with Python 3.14 and 3.12, nvm with Node 24 and 22. Anything else would need
// an extra install in the Dockerfile. Bump these together with BenchImageTag.
var (
	ImagePythons = []string{"3.12", "3.14"}
	ImageNodes   = []string{"22", "24"}
)

// ToolchainFor returns the toolchain for a Frappe branch.
//
// version-15 allows Python >=3.10,<3.15 and Node >=18, and is tested upstream on
// older versions than the image default, so it gets the image's previous pair.
// version-16 requires Python 3.14 and Node 24. Every other branch (develop, a
// fork's main) gets the newest pair; pass --python/--node to override.
func ToolchainFor(branch string) Toolchain {
	if strings.HasPrefix(branch, "version-15") {
		return Toolchain{Python: "3.12", Node: "22"}
	}
	return Toolchain{Python: "3.14", Node: "24"}
}

// ValidateFor refuses versions the bench image does not ship, and versions the
// Frappe branch cannot run on: version-16 requires Python 3.14 and Node 24,
// and bench init would only fail on them minutes in.
func (t Toolchain) ValidateFor(branch string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if strings.HasPrefix(branch, "version-16") && (t.Python != "3.14" || t.Node != "24") {
		return fmt.Errorf("Frappe version-16 requires Python 3.14 and Node 24 (got Python %s, Node %s)", t.Python, t.Node)
	}
	return nil
}

// Validate refuses versions the bench image does not ship.
func (t Toolchain) Validate() error {
	if !slices.Contains(ImagePythons, t.Python) {
		return fmt.Errorf("unsupported Python %q: the bench image ships %s", t.Python, strings.Join(ImagePythons, " and "))
	}
	if !slices.Contains(ImageNodes, t.Node) {
		return fmt.Errorf("unsupported Node %q: the bench image ships %s", t.Node, strings.Join(ImageNodes, " and "))
	}
	return nil
}

// PythonBin is the interpreter bench init builds the virtualenv from. The
// pyenv shims resolve it because the image lists every shipped version in
// `pyenv global`.
func (t Toolchain) PythonBin() string { return "python" + t.Python }
