package main

import (
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
