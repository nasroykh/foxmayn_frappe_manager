package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// openBrowser opens url in the desktop's browser. It fails where there is no
// desktop to open it on (an SSH session, a headless server, CI), and callers
// then print the URL instead.
func openBrowser(url string) error {
	if !isInteractive() {
		return errors.New("no interactive terminal")
	}
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("WSL_DISTRO_NAME") == "" {
			return errors.New("no graphical session")
		}
		name = "xdg-open"
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			name = "wslview"
		}
	}
	cmd := execx.Command(name, append(args, url)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w %s", name, err, out)
	}
	return nil
}

// showURL opens url unless printOnly, and prints it when it was not opened.
func showURL(url string, printOnly bool) error {
	if !printOnly {
		if err := openBrowser(url); err == nil {
			fmt.Println("Opened " + url)
			return nil
		}
	}
	fmt.Println(url)
	return nil
}
