# Camflow

Camflow is a CLI tool designed to automate the photography workflow for enthusiasts who use Google Photos. It handles the tedious parts of moving files from your camera to your computer and from your computer to the cloud, while preserving your freedom to use your own editing tools in between.

## The Workflow

Camflow supports a "Curate & Edit" philosophy for photos, while keeping video management simple.

1.  **Import**: `camflow import` moves media off your SD card.
    *   **Photos** go to an "Inbox" (`photos_to_process_root`) for you to edit.
    *   **Videos** go straight to an "Upload Queue" (`videos_export_queue_root`), as they are rarely edited.
2.  **Process (Photos)**: You use your favorite tool (Lightroom, Capture One, Photo Mechanic) to review the Inbox. Delete the bad shots, edit the good ones, and export your final JPEGs to the "Photo Upload Queue" (`photos_export_queue_dir`).
3.  **Upload (Videos)**: You drag-and-drop videos from the queue to the Google Photos website (to preserve metadata), then run `camflow mark-videos-exported` to archive them locally.
4.  **Upload (Photos)**: Run `camflow upload-photos` to upload your finished JPEGs to Google Photos and archive them locally.

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
#    Structure: Organized into date-based subfolders (YYYY/MM/DD/).
photos_to_process_root = "/Users/you/Pictures/Camflow/Inbox"
# 2. Upload source: Where you place curated/edited JPGs ready for upload.
#    Structure: Flat (all files directly in this folder).
photos_export_queue_dir = "/Users/you/Pictures/Camflow/UploadQueue"
# 3. Archive: Where photos are moved after successful upload.
#    Structure: Organized into date-based subfolders (YYYY/MM/DD/).
photos_exported_root = "/Users/you/Pictures/Camflow/Archive"

# Videos Flow
# Videos skip the inbox and go straight to the queue.
# Structure: Flat (all files directly in this folder).
videos_export_queue_root = "/Users/you/Movies/Camflow/UploadQueue"

# Structure: Organized into date-based subfolders (YYYY/MM/DD/).
videos_exported_root = "/Users/you/Movies/Camflow/Archive"
```

## Usage

### 1. Import from SD Card
Copies media from your SD card to your computer and deletes the originals from the card.
*   **Photos** go to your `photos_to_process_root` (Inbox), organized by date (YYYY/MM/DD).
*   **Videos** go directly into the root of `videos_export_queue_root` (Upload Queue).

```bash
# Import (copy and delete) all media from a camera's SD card, into the
# 
# Useful flags:
# --keep:
#     Don't delete media from the SD card.
#     Defaults to false (does delete source media).
# --src:
#     Path to the SD card.
#     Defaults to "/Volumes/EOS_DIGITAL", for Canon cameras mounted in macOS.
camflow import --src /Volumes/EOS_DIGITAL
```

### 2. Curate & Edit Photos (Manual Step)
Open your `photos_to_process_root` folder with your editing software (e.g., Lightroom).
1.  **Filter**: Review your photos and delete the rejects.
2.  **Edit**: Process your RAW files.
3.  **Export**: Export the final JPEGs you want to upload into the `photos_export_queue_dir`.
    *   *Note: You may be exporting JPEGs from RAW sources; Camflow handles this fine.*

### 3. Upload Videos (Manual Step)
*Currently, automated video upload has a bug with timestamps, so the manual workflow is recommended.*

1.  Open your `videos_export_queue_root` folder.
2.  Drag and drop the video files into the Google Photos website.
3.  Run the following command to move the videos to your archive folder:
    ```bash
    camflow mark-videos-exported
    ```

### 4. Upload Photos
Run the automated uploader for your processed photos. This command:
1.  Uploads files from `photos_export_queue_dir`.
2.  Sorts them into albums based on your config and file metadata (Labels/Subjects).
3.  Moves the local files to `photos_exported_root` (Archive).

```bash
camflow upload-photos
```

## Recommended Setup & Tips

### Cloud Storage for Archives
Over time, your `exported_root` (Archive) folders will grow very large. A good strategy is to point these paths to a directory synced with cloud storage (e.g., a mounted Google Drive folder).
*   **Benefit**: Your archives are automatically backed up off-site.
*   **Benefit**: You can access your original high-res exports via the cloud provider's API (which Google Photos does not offer).

### Using Metadata to Organize Albums (Lightroom Example)
Camflow can organize uploads into specific Google Photos albums based on image metadata. However, the Google Photos API has limitations: it cannot "Favorite" photos, and it cannot add photos to albums it didn't create.

You can use "workflow albums" to get around this:

**1. Workaround for Favorites**
*   **Setup**: Configure Camflow to map a metadata Label (eg, "Red") to a special Google Photos album (eg, "Camflow: Favorite").
*   **Workflow**: Mark your best shots with the Red label in Lightroom.
*   **After Upload**: Go to the "Camflow: Favorite" album in Google Photos, favorite each photo, select all the photos and remove them from the album.

**2. Workaround for Shared Albums**
*   **Setup**: Configure Camflow to map a metadata Subject (eg, "share-family") to a Google Photos album (eg, "Camflow: Family Album").
*   **Workflow**: Add the subject "share-family" to relevant photos.
*   **After Upload**: Go to the "Camflow: Family Album". Select all -> Add to your actual "Shared Family Album" -> Remove from "Camflow: Family Album".

