package cli

import "github.com/spf13/cobra"

func AddCommands(cmd *cobra.Command, cli CLI) {
	cmd.AddCommand(NewVersionCmd(cli))

	hostCmd := NewHostCmd(cli)
	hostCmd.AddCommand(NewHostProtectedidCmd(cli))
	hostCmd.AddCommand(NewHostPreflightCmd(cli))
	cmd.AddCommand(hostCmd)

	rookCmd := NewRookCmd(cli)
	rookCmd.AddCommand(NewHostpathToBlockCmd(cli))
	rookCmd.AddCommand(NewRookHealthCmd(cli))
	cmd.AddCommand(rookCmd)

	cmd.AddCommand(NewSyncObjectStoreCmd(cli))

	cmd.AddCommand(NewFormatAddressCmd(cli))
}
