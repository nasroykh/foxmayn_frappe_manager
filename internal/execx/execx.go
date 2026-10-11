// Package execx is the single place ffm creates external commands (docker,
// crontab). Production code calls execx.Command; tests replace it with a fake
// (see execx/fakeexec) so pipelines can be exercised without Docker.
package execx

import "os/exec"

// Command is exec.Command, replaceable in tests.
var Command = exec.Command

// LookPath is exec.LookPath, replaceable in tests (the fake commands run
// through Command, but whether a program exists on PATH is not a command).
var LookPath = exec.LookPath
