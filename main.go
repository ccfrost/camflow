package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath" // Added for filepath.Dir
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/ccfrost/camedia/commands"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v3"
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
			if err := config.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
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

			res, err := commands.Import(config, srcDir, keep, time.Now())
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}

			optColon := ""
			if len(res.Photos) > 0 {
				optColon = ":"
			}
			fmt.Printf("Imported %d photos:\n", len(res.Photos))
			for dirName, count := range res.Photos {
				fmt.Printf("\t%s: %d photos\n", dirName, count)
			}

			optColon = ""
			if len(res.Videos) > 0 {
				optColon = ":"
			}
			fmt.Printf("Imported %d videos%s\n", optColon, len(res.Videos))
			for dirName, count := range res.Videos {
				fmt.Printf("\t%s: %d videos\n", dirName, count)
			}
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

			ctx := context.Background()
			configDir := filepath.Dir(config.ConfigPath())
			gphotosHttpClient, err := commands.GetAuthenticatedGooglePhotosClient(ctx, config, configDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			gphotosClient, err := gphotos.NewClient(gphotosHttpClient)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			if err := commands.UploadVideos(ctx, config, configDir, keep, gphotosClient); err != nil {
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
