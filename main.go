package main

import (
	"fmt"
	"os"
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/ccfrost/camedia/commands"
	"github.com/spf13/cobra"
)

const camedia = "camedia"

func main() {
	var configPath string
	var config camediaconfig.CamediaConfig

	rootCmd := cobra.Command{
		Use:   camedia,
		Short: "Manage camera media files",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			config, err = camediaconfig.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "Path to the configuration file")

	// TODO: describe the args, for all commands.

	// TODO: add version command.

	importCmd := cobra.Command{
		Use:   "import",
		Short: "Import media from the sdcard",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// TODO: find sd card (diskutil/gemini code)
			srcDir := "/Volumes/sdcardTODO"
			keep, err := cmd.Flags().GetBool("keep")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid keep flag:", err)
				os.Exit(1)
			}
			relTargetDir, err := commands.Import(config, srcDir, keep, time.Now())
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			// TODO: don't say if there were no videos:
			fmt.Println("imported photos to", relTargetDir)
			// TODO: if there were videos, say to run upload command.
		},
	}
	importCmd.Flags().BoolP("keep", "k", false, "Keep the source files")
	rootCmd.AddCommand(&importCmd)

	// TODO: add upload-photos command.

	// TODO: add upload-videos command.

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
