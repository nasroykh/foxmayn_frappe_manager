// Package execx is the single place ffm creates external commands (docker,
// crontab). Production code calls execx.Command; tests replace it with a fake
// (see execx/fakeexec) so pipelines can be exercised without Docker.
package execx

import "os/exec"

// Command is exec.Command, replaceable in tests.
var Command = exec.Command
