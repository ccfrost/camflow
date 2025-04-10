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

	rootCmd.AddCommand(&cobra.Command{
		Use:   "import",
		Short: "Import media from the sdcard",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// TODO: find sd card (diskutil/gemini code)
			srcDir := "/Volumes/sdcardTODO"
			relTargetDir, err := commands.Import(config, srcDir, time.Now())
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			// TODO: don't say if there were no videos:
			fmt.Println("imported photos to", relTargetDir)
			// TODO: if there were videos, say to run upload command.
		},
	})

	// TODO: add upload-photos command.

	// TODO: add upload-videos command.

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
