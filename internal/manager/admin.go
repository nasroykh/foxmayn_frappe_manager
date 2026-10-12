package manager

import (
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// setAdminPassword sets the Administrator password of a site through
// `bench set-admin-password`, handing the password over on stdin.
//
// The command takes it as a positional argument, and bench execs the frappe
// interpreter, so an argument would sit on that process's argument list — in
// the container and, through `ps`, on the host — for as long as it runs. With no
// argument Frappe asks with getpass, which reads a line from stdin when there is
// no terminal. The password is one line: ValidateAdminPassword refuses others.
func setAdminPassword(runner *bench.Runner, siteName, password string) error {
	if strings.ContainsAny(password, "\n\r\x00") {
		return fmt.Errorf("the Administrator password must be a single line")
	}
	cmd := "cd /workspace/frappe-bench && bench --site " + bench.ShellQuote(siteName) + " set-admin-password"
	out, err := runner.ExecStdinOut("frappe", strings.NewReader(password+"\n"), "bash", "-c", cmd)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
