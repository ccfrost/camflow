package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const camedia = "camedia"

func main() {
	// TODO: remove workingDir?
	var workingDir string

	rootCmd := cobra.Command{
		Use:   camedia,
		Short: "Manage camera media files",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if workingDir == "" {
				var err error
				workingDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get working dir: %w", err)
				}
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&workingDir, "working-store", "", "Path to the working store")

	// TODO: describe the args, for all commands.

	// TOOD: maybe: abort if there is an operation running or to resume

	/*
		rootCmd.AddCommand(&cobra.Command{
			Use:   "init",
			Short: "Init camedia",
			Args:  cobra.ExactArgs(1),
			Run: func(cmd *cobra.Command, args []string) {
				if err := commands.InitLibrary(workingDir, args[0]); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					os.Exit(1)
				}
			},
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "import",
			Short: "Import media from the sdcard",
			Args:  cobra.ExactArgs(1),
			Run: func(cmd *cobra.Command, args []string) {
				relTargetDir, err := commands.Import(workingDir, time.Now(), args[0])
				if err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					os.Exit(1)
				}
				fmt.Println("imported to", relTargetDir)
			},
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "release",
			Short: "Release a directory from the working store (dir remains in the archvie store)",
			Args:  cobra.ExactArgs(1),
			Run: func(cmd *cobra.Command, args []string) {
				if err := commands.ReleaseWorking(workingDir, args[0]); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					os.Exit(1)
				}
			},
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "load",
			Short: "Load a directory from the archive into the working store",
			Args:  cobra.ExactArgs(1),
			Run: func(cmd *cobra.Command, args []string) {
				if err := commands.LoadWorking(workingDir, args[0]); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					os.Exit(1)
				}
			},
		})
	*/

	// TODO: publish/export command? what would the integration with lightroom, gphotos be? pause/throttle? mark as complete in sheet or own db?
	// TODO: add a resume command?
	// TODO: add a command to show archive dir path, show version of metadata store, show whether this is a pending command
	// TODO: add a check-consistency command?

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
