# Camflow

Camflow is a CLI tool designed to automate the photography workflow for enthusiasts who use Google Photos. It handles the tedious parts of moving files from your camera to your computer and from your computer to the cloud, while preserving your freedom to use your own editing tools in between.

## The Workflow

Camflow is built around a "Curate & Edit" philosophy:

1.  **Import**: `camflow import` moves photos and videos from your SD card to a local "Staging" directory, organized by date.
2.  **Curate**: You browse the staging directory with your favorite tool (Lightroom, Finder, Photo Mechanic, etc.). Delete the blurry shots, edit the good ones, and save your final JPGs there.
3.  **Upload**: `camflow upload-photos` scans the staging directory and uploads your curated collection to Google Photos.

## Installation

Currently, Camflow must be built from source. Ensure you have [Go](https://go.dev/dl/) installed.

```bash
go install github.com/ccfrost/camflow@latest
```

## Configuration

Before running Camflow, you need to set up a `config.toml` file.

### 1. Google Cloud Credentials
To upload to your Google Photos account, you must create a Google Cloud Project and generate OAuth 2.0 credentials (Client ID and Client Secret).

> **Guide:** Follow this excellent tutorial to obtain your `client_id` and `client_secret`:
> [https://gilesknap.github.io/gphotos-sync/main/tutorials/oauth2.html](https://gilesknap.github.io/gphotos-sync/main/tutorials/oauth2.html)

### 2. Configuration File

Create a file named `config.toml` in your configuration directory (e.g., `~/.config/camflow/` or the directory you run the tool from).

```toml
# config.toml

# Google Photos API Credentials
[google_photos]
client_id = "YOUR_CLIENT_ID.apps.googleusercontent.com"
client_secret = "YOUR_CLIENT_SECRET"
redirect_uri = "http://localhost:8080" 

[google_photos.photos]
default_album = "Camera Uploads"

[google_photos.videos]
default_album = "Videos"

# Local Workflow Paths (Use absolute paths)

# Photos Flow
# 1. Import destination: Where raw photos land for curation.
photos_to_process_root = "/Users/you/Pictures/Camflow/Inbox"
# 2. Upload source: Where you place curated/edited JPGs ready for upload.
photos_export_queue_dir = "/Users/you/Pictures/Camflow/UploadQueue"
# 3. Archive: Where photos are moved after successful upload.
photos_exported_root = "/Users/you/Pictures/Camflow/Archive"

# Videos Flow
# Videos skip the inbox and go straight to the queue.
videos_export_queue_root = "/Users/you/Movies/Camflow/UploadQueue"
videos_exported_root = "/Users/you/Movies/Camflow/Archive"
```

## Usage

### 1. Import from SD Card
Copies media from your SD card.
*   **Photos** go to your `photos_to_process_root` (Inbox).
*   **Videos** go directly to `videos_export_queue_root` (Upload Queue).

```bash
# Import from the default  (Defaults to /Volumes/EOS_DIGITAL)
camflow import

# Import (copy and delete) all media from a camera's SD card.
# Useful flags:
# --keep:
#     Don't delete media from the SD card.
#     Defaults to false (does delete source media).
# --src:
#     Path to the SD card.
#     Defaults to "/Volumes/EOS_DIGITAL", for Canon cameras mounted in macOS.
camflow import --src /Volumes/EOS_DIGITAL
```

### 2. Curate & Edit (Manual Step)
Open your `photos_to_process_root` folder.
1.  Review your photos. Delete the bad ones.
2.  Edit them if you wish.
3.  **Move** the final JPGs you want to upload into the `photos_export_queue_dir`.

### 3. Upload Photos
Uploads all images found in `photos_export_queue_dir` to Google Photos.
*   uploaded files are moved to `photos_exported_root` (Archive).

```bash
camflow upload-photos
```

### 4. Upload Videos
Uploads all videos found in `videos_export_queue_root`.
*   uploaded files are moved to `videos_exported_root` (Archive).

```bash
camflow upload-videos
```

### Manage Videos Manually
If you prefer to manage video uploads yourself (e.g., via the web interface to preserve specific metadata), you can move them to the archive folder without uploading:
```bash
camflow mark-videos-exported
```
