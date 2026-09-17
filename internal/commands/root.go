// Package commands is the command line.
package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/printer"
	"github.com/ziyan/cf/internal/version"
)

var rootCommand = &cobra.Command{
	Use:     "cf",
	Version: version.Version(),
	Short:   "Archive and search a Confluence site",
	Long: "Keep a local copy of the pages and comments you can read, and search it " +
		"without going back to the server.\n\n" +
		"A sync is incremental: the first run reads the site in full, later runs ask " +
		"only for what changed since, so re-running is cheap and safe.",
	PersistentPreRun: func(command *cobra.Command, arguments []string) {
		jsonFlag, _ := command.Flags().GetBool("json")
		printer.JSONOutput = jsonFlag
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCommand.PersistentFlags().Bool("json", false, "Output in JSON format")
	rootCommand.PersistentFlags().String("profile", "", "Use a named profile instead of the active one")
	rootCommand.SetVersionTemplate(fmt.Sprintf("cf version %s (commit %s)\n", version.Version(), version.Commit()))
}

// Execute runs the command line.
func Execute() error {
	return rootCommand.Execute()
}
