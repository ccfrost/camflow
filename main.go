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

	// TODO: add version command.

	importCmd := cobra.Command{
		Use:   "import",
		Short: "Import media from the sdcard",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			srcDir, err := cmd.Flags().GetString("src")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid src flag:", err)
				os.Exit(1)
			}
			if srcDir == "" {
				// TODO: find sd card (diskutil/gemini code)
				panic("TODO: find sd card")
			}

			var keep bool
			keep, err = cmd.Flags().GetBool("keep")
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
	importCmd.Flags().StringP("src", "s", "/Volumes/EOS_DIGITAL/", "Path to the source sdcard directory (defaults to auto-detect)")
	importCmd.Flags().BoolP("keep", "k", false, "Keep the source files")
	rootCmd.AddCommand(&importCmd)

	// TODO: add upload-photos command.

	uploadVideosCmd := cobra.Command{
		Use:   "upload-videos",
		Short: "Upload videos from staging to Google Photos",
		Long: `Upload videos from the staging directory to Google Photos.
Videos will be added to all albums configured in default_albums.
Successfully uploaded videos are deleted from staging unless --keep is specified.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			keep, err := cmd.Flags().GetBool("keep")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid keep flag:", err)
				os.Exit(1)
			}

			if err := commands.UploadVideos(config, keep); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		},
	}
	uploadVideosCmd.Flags().BoolP("keep", "k", false, "Keep videos in staging after upload")
	rootCmd.AddCommand(&uploadVideosCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
