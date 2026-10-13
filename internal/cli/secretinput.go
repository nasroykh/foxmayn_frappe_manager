package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
)

// readSecretLine reads one line from r and strips the line ending only: a
// password may start or end with spaces.
func readSecretLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// secretInput returns a secret that must never travel as a flag value, from
// the first source that has one:
//
//  1. stdin, when fromStdin is set (--password-stdin);
//  2. the first non-empty environment variable of envs;
//  3. a hidden prompt, asked twice, when a person is at the terminal.
//
// Without any of them the command fails naming the flag, rather than hanging.
func secretInput(fromStdin bool, envs []string, what, stdinFlag string) (string, error) {
	if fromStdin {
		v, err := readSecretLine(stdinReader())
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", usageError{fmt.Errorf("%s from stdin is empty", what)}
		}
		return v, nil
	}
	for _, e := range envs {
		if v := os.Getenv(e); v != "" {
			return v, nil
		}
	}
	if !isInteractive() {
		return "", mustNotPrompt(what, "pass "+stdinFlag+" (and pipe the value in) or set $"+envs[0])
	}
	var first, second string
	err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(capitalize(what)).EchoMode(huh.EchoModePassword).Value(&first),
		huh.NewInput().Title("Repeat it").EchoMode(huh.EchoModePassword).Value(&second),
	)).WithKeyMap(benchPickKeyMap()).Run()
	if err != nil {
		if cancelled(err) {
			return "", errCancelled
		}
		return "", err
	}
	if first != second {
		return "", errors.New("the two entries differ")
	}
	if first == "" {
		return "", usageError{fmt.Errorf("%s cannot be empty", what)}
	}
	return first, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// errCancelled is returned when the person backed out of a prompt; callers
// turn it into a clean exit.
var errCancelled = errors.New("cancelled")

// stdinReader is os.Stdin; tests replace it.
var stdinReader = func() io.Reader { return os.Stdin }
