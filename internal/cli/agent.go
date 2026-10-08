package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent [bench]",
		Short: "Turn a dev bench's agent-ready profile on or off (bare: show it)",
		Long: `An agent-ready bench gives ffc and the MCP server a dedicated System Manager
user (agent@<site>) instead of Administrator, so the agent's changes are
attributed to it and its keys can be revoked alone. --read-only limits the
MCP server to read tools. The bench must publish its ports on 127.0.0.1 and
not forward your SSH agent; a default admin password is replaced by a random
one. Create one directly with 'ffm create --agent'.

'off' disables the agent user and puts ffc and MCP back on Administrator.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			b, err := manager.New(verbose).GetBench(name)
			if err != nil {
				return err
			}
			switch {
			case b.Agent && b.AgentReadOnly:
				fmt.Printf("Bench %q is agent-ready (MCP read-only): ffc and MCP act as agent@%s.\n", name, b.SiteName)
			case b.Agent:
				fmt.Printf("Bench %q is agent-ready: ffc and MCP act as agent@%s.\n", name, b.SiteName)
			default:
				fmt.Printf("Bench %q is not agent-ready: ffc and MCP act as Administrator.\n", name)
			}
			return nil
		},
	}
	var readOnly bool
	on := &cobra.Command{
		Use:   "on [bench]",
		Short: "Give ffc and MCP a dedicated agent user",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			return manager.New(verbose).SetAgent(manager.SetAgentInput{Name: name, On: true, ReadOnly: readOnly}, manager.CLIProgress{})
		},
	}
	on.Flags().BoolVar(&readOnly, "read-only", false, "Limit the MCP server to read tools")
	off := &cobra.Command{
		Use:   "off [bench]",
		Short: "Put ffc and MCP back on Administrator and disable the agent user",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			return manager.New(verbose).SetAgent(manager.SetAgentInput{Name: name, On: false}, manager.CLIProgress{})
		},
	}
	cmd.AddCommand(on, off)
	return cmd
}
