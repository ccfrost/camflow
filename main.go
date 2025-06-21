package main

import (
	"context"
	"fmt"
	"os" // Added for filepath.Dir
	"time"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/ccfrost/camflow/commands"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/spf13/cobra"
)

const camflow = "camflow"

func main() {
	var configPathFlag, cacheDirFlag string
	var config camflowconfig.CamflowConfig

	rootCmd := cobra.Command{
		Use:   camflow,
		Short: "Manage camera media files",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			config, err = camflowconfig.LoadConfig(configPathFlag)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			if err := config.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			return nil
		},
	}
	// TODO: set defaults here, to make them more discoverable for users.
	rootCmd.PersistentFlags().StringVarP(&configPathFlag, "config", "c", "", "Path to the configuration file")
	rootCmd.PersistentFlags().StringVar(&cacheDirFlag, "cache-dir", "", "Dir to store cache files")

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

			// TODO: change relative dirs to print target rather than sdcard dir names (and counts?).
			optColon := ""
			if len(res.SrcEntries) > 0 {
				optColon = ":"
			}
			fmt.Printf("Imported from %d dir%s%s\n", len(res.SrcEntries), pluralSuffix(len(res.SrcEntries)), optColon)
			if len(res.SrcEntries) != 0 {
				for _, entry := range res.SrcEntries {
					fmt.Printf("\t%s: %d photo%s, %d video%s\n", entry.RelativeDir, entry.PhotoCount, pluralSuffix(entry.PhotoCount), entry.VideoCount, pluralSuffix(entry.VideoCount))
				}
				fmt.Printf("Imported into %d dir%s:\n", len(res.DstEntries), pluralSuffix(len(res.DstEntries)))
				for _, entry := range res.DstEntries {
					fmt.Printf("\t%s: %d photo%s, %d video%s\n", entry.RelativeDir, entry.PhotoCount, pluralSuffix(entry.PhotoCount), entry.VideoCount, pluralSuffix(entry.VideoCount))
				}
			}
		},
	}
	importCmd.Flags().StringP("src", "s", "/Volumes/EOS_DIGITAL/", "Path to the source sdcard directory (defaults to auto-detect)")
	importCmd.Flags().BoolP("keep", "k", false, "Keep the source files")
	rootCmd.AddCommand(&importCmd)

	uploadPhotosCmd := cobra.Command{
		Use:   "upload-photos",
		Short: "Upload photos from export queue to Google Photos",
		Long: `Upload photos from the export queue to Google Photos.
Successfully uploaded photos are deleted from staging unless --keep is specified.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			keep, err := cmd.Flags().GetBool("keep")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid keep flag:", err)
				os.Exit(1)
			}

			ctx := context.Background()
			gphotosHttpClient, err := commands.GetAuthenticatedGooglePhotosClient(ctx, config, cacheDirFlag)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			gphotosClient, err := gphotos.NewClient(gphotosHttpClient)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			wrappedGphotosClient := commands.NewGPhotosClientWrapper(gphotosClient)

			if err := commands.UploadPhotos(ctx, config, cacheDirFlag, keep, wrappedGphotosClient); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		},
	}
	uploadPhotosCmd.Flags().BoolP("keep", "k", false, "Keep photos in staging after upload")
	rootCmd.AddCommand(&uploadPhotosCmd)

	uploadVideosCmd := cobra.Command{
		Use:   "upload-videos",
		Short: "Upload videos from export queue to Google Photos",
		Long: `Upload videos from the export queue to Google Photos.
Successfully uploaded videos are deleted from staging unless --keep is specified.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			keep, err := cmd.Flags().GetBool("keep")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid keep flag:", err)
				os.Exit(1)
			}

			ctx := context.Background()
			gphotosHttpClient, err := commands.GetAuthenticatedGooglePhotosClient(ctx, config, cacheDirFlag)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			gphotosClient, err := gphotos.NewClient(gphotosHttpClient)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			wrappedGphotosClient := commands.NewGPhotosClientWrapper(gphotosClient)

			if err := commands.UploadVideos(ctx, config, cacheDirFlag, keep, wrappedGphotosClient); err != nil {
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

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
