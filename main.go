package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ccfrost/camflow/internal/config"
	"github.com/ccfrost/camflow/internal/lib"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/spf13/cobra"
)

const camflow = "camflow"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var configPath, cacheDir string
	var cfg config.CamflowConfig

	rootCmd := cobra.Command{
		Use:   camflow,
		Short: "Manage camera media files",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			cfg, err = config.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			return nil
		},
	}
	{
		defaultConfigPath, err := config.DefaultConfigPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: unable to determine default config path:", err)
			os.Exit(1)
		}
		rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", defaultConfigPath, "Path to the configuration file")

		defaultCacheDir, err := DefaultCacheDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: unable to determine default cache dir:", err)
			os.Exit(1)
		}
		rootCmd.PersistentFlags().StringVar(&cacheDir, "cache-dir", defaultCacheDir, "Dir to store cache files")
	}

	versionCmd := cobra.Command{
		Use:   "version",
		Short: "Print the version number of camflow",
		Run: func(cmd *cobra.Command, args []string) {
			// If any version info is missing, try to read it from the binary's build info.
			if version == "dev" || commit == "none" {
				if info, ok := debug.ReadBuildInfo(); ok {
					if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
						version = info.Main.Version
					}

					modified := false
					for _, setting := range info.Settings {
						switch setting.Key {
						case "vcs.revision":
							if commit == "none" {
								commit = setting.Value
							}
						case "vcs.modified":
							modified = (setting.Value == "true")
						case "vcs.time":
							if date == "unknown" {
								date = setting.Value
							}
						}
					}
					if modified {
						commit += " (dirty)"
					}
				}
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Client:\t%s\n", camflow)
			fmt.Fprintf(w, "Version:\t%s\n", version)
			fmt.Fprintf(w, "Go version:\t%s\n", runtime.Version())
			fmt.Fprintf(w, "Git commit:\t%s\n", commit)
			fmt.Fprintf(w, "Built:\t%s\n", date)
			fmt.Fprintf(w, "OS/Arch:\t%s/%s\n", runtime.GOOS, runtime.GOARCH)
			w.Flush()
		},
	}
	rootCmd.AddCommand(&versionCmd)

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

			res, err := lib.Import(cfg, srcDir, keep, time.Now())
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
				fmt.Printf("Imported photos into %d dir%s:\n", len(res.DstEntries), pluralSuffix(len(res.DstEntries)))
				for _, entry := range res.DstEntries {
					fmt.Printf("\t%s: %d photo%s\n", entry.RelativeDir, entry.PhotoCount, pluralSuffix(entry.PhotoCount))
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
			gphotosHttpClient, err := lib.GetAuthenticatedGooglePhotosClient(ctx, cfg, cacheDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			gphotosClient, err := gphotos.NewClient(gphotosHttpClient)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			wrappedGphotosClient := lib.NewGPhotosClientWrapper(gphotosClient)

			if err := lib.UploadPhotos(ctx, cfg, cacheDir, keep, wrappedGphotosClient); err != nil {
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
			gphotosHttpClient, err := lib.GetAuthenticatedGooglePhotosClient(ctx, cfg, cacheDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			gphotosClient, err := gphotos.NewClient(gphotosHttpClient)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			wrappedGphotosClient := lib.NewGPhotosClientWrapper(gphotosClient)

			if err := lib.UploadVideos(ctx, cfg, cacheDir, keep, wrappedGphotosClient); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		},
	}
	uploadVideosCmd.Flags().BoolP("keep", "k", false, "Keep videos in staging after upload")
	rootCmd.AddCommand(&uploadVideosCmd)

	markVideosExportedCmd := cobra.Command{
		Use:   "mark-videos-exported",
		Short: "Move videos from export queue to exported directory without uploading",
		Long: `Move videos from the export queue to the exported directory.
This is a workaround for video uploads not preserving the video's timezone.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Confirm with user to protect against accidental invocation.
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Confirm: move all videos in export queue to the exported directory? [y/N]: ")
			response, err := reader.ReadString('\n')
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: failed to read confirmation:", err)
				os.Exit(1)
			}

			response = strings.TrimSpace(strings.ToLower(response))
			if response != "y" && response != "yes" {
				fmt.Println("Aborted")
				return
			}

			ctx := context.Background()
			if err := lib.MarkVideosExported(ctx, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		},
	}
	rootCmd.AddCommand(&markVideosExportedCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// DefaultCacheDir returns the default cache directory.
func DefaultCacheDir() (string, error) {
	// Use the default user cache directory.
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user cache dir: %w", err)
	}
	return filepath.Join(dir, "camflow"), nil
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
