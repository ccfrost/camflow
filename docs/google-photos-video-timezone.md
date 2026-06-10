# Google Photos drops the capture timezone on videos uploaded via the Library API

If you upload videos to Google Photos through the **Library API** and they show up
at the **wrong time** — shifted by your UTC offset, often onto the previous day —
this explains why, what does *not* fix it, and what does. Photos (JPEG) are
unaffected; this is video-only, and in practice it only bites **camera-recorded
MP4s**, not phone videos.

Keywords (so this is findable): google photos api video wrong time / wrong
timezone / off by hours / previous day, `mediaMetadata.creationTime`, MP4 vs MOV,
`com.apple.quicktime.creationdate`, ftyp brand `mp42` vs `qt`.

## TL;DR

- **Cause:** Google honors a video's timezone offset only when the file's container
  is **QuickTime** (`ftyp` major brand `qt`). For an **MP4** brand (`mp42`) — what
  Canon and many cameras write — it mis-parses the timestamp and the displayed time
  lands off by the offset. **It's the container, not the metadata.**
- **Fix:** losslessly **remux MP4 → MOV** (`ffmpeg -c copy`, no re-encode) and add a
  `com.apple.quicktime.creationdate` atom carrying the offset. Nothing you do at the
  metadata layer alone works.
- **Biggest trap:** the API's `creationTime` and the web UI show a **transient**
  value right after upload that *settles* to the (wrong) value 30–60+ minutes later.
  Checking too early gives a false "it works." **Only trust the settled value,
  >1 hour after upload.**

## Worked example

A Canon clip shot at **2026-06-06 07:55:00 −08:00** (true instant
`2026-06-06 15:55:00Z`):

| | what Google stores as `creationTime` | what the UI shows |
|---|---|---|
| **Correct** | `2026-06-06T15:55:00Z` | Jun 6, 7:55 AM |
| **The bug** | `2026-06-06T07:55:00Z` (local wall-clock mislabelled as `Z`) | Jun **5**, 11:55 PM |

Google takes the local wall-clock from the file and treats it as if it were UTC,
then the UI re-applies the offset — so the time ends up wrong by (roughly) the
offset, here landing on the previous day.

## Why only videos (and only camera videos)

- **Photos work** because Google reads the offset from JPEG EXIF
  (`OffsetTimeOriginal`). For **video** it ignores EXIF and reads the
  QuickTime/MP4 atoms instead.
- The standard MP4 date atoms (`mvhd`, `tkhd`, `mdhd`) are UTC-only by spec — no
  timezone field. Apple added `com.apple.quicktime.creationdate` (in `moov/meta`,
  handler `mdta`) to carry an explicit offset like `2026-06-06T07:55:00-08:00`, and
  Google honors it — **but only in a QuickTime-brand container.**
- **Phone videos are already QuickTime** (`.mov`, brand `qt`), so they display
  correctly and nobody notices. The bug only surfaces for camera MP4s, which is why
  it's underreported.

## What does NOT fix it

We tested each of these and checked the **settled** `creationTime` hours later. All
of them still mangle, because they only change metadata, not the container brand:

| Attempt | container | settled `creationTime` | result |
|---|---|---|---|
| Add the `com.apple.quicktime.creationdate` atom (offset correct) | `mp42` | `…07:55:00Z` | ❌ wrong |
| Atom **+ rewrite `mvhd`/track dates to local time** (mimic iPhone) | `mp42` | `…07:55:00Z` | ❌ wrong |
| Atom **+ strip all EXIF / maker-note / GPS** metadata | `mp42` | `…07:55:00Z` | ❌ wrong |

Notably, the atom + local-`mvhd` and the fully-stripped files looked **correct for
the first ~50 minutes**, then drifted to the wrong value — see the verification trap
below. Do not be fooled.

(Also: do **not** end up with two `meta` blocks in `moov` — e.g. from an ffmpeg
trim that added an empty one with `-movflags use_metadata_tags` — Google picks the
empty one and ignores your atom entirely. A clean single-pass remux avoids this.)

## What DOES fix it

Make the file a **QuickTime container** and carry the offset in the atom:

| Attempt | container | settled `creationTime` | result |
|---|---|---|---|
| Same camera content **remuxed to `.mov`** + atom | `qt` | `…15:55:00Z` | ✅ correct |
| A genuine iPhone `.mov` (positive control) | `qt` | true instant | ✅ correct |

Recipe (lossless — no re-encode):

```sh
# 1. Remux MP4 -> QuickTime .mov ('qt' brand). -map 0 keeps every stream; -c copy
#    rewraps without re-encoding (near-instant, no quality loss).
ffmpeg -nostdin -y -loglevel error -i in.MP4 -map 0 -c copy out.mov

# 2. Add the creationdate atom (local wall-clock + offset) and restore the mvhd
#    create/modify dates, which ffmpeg zeroes, to the true UTC instant.
#    QuickTimeUTC=0 makes exiftool store these literally, without its own UTC shift.
exiftool -api QuickTimeUTC=0 -overwrite_original \
  '-Keys:CreationDate=2026:06:06 07:55:00-08:00' \
  '-QuickTime:CreateDate=2026:06:06 15:55:00' \
  '-QuickTime:ModifyDate=2026:06:06 15:55:00' \
  out.mov
```

The offset values come from the camera's own EXIF (`DateTimeOriginal` +
`OffsetTimeOriginal`); the UTC instant is just `DateTimeOriginal` converted by the
offset. Upload the `.mov`. Google now displays the correct local time.

## How to verify — and the trap that fools everyone

**Google reports a transient timestamp right after upload, then settles to a
different one 30–60+ minutes later.** A correct-looking value at 5 minutes means
nothing. This is the single biggest time-sink here — it produced false "it works"
verdicts twice during this investigation.

Verify like this:

- Read the **settled** value, **>1 hour** after upload (ideally next day), via
  `GET /v1/mediaItems/{id}` → `mediaMetadata.creationTime`. **Trust the settled API
  value, not the immediate UI.**
- Correct = the **true UTC instant** (`…15:55:00Z`). Bug = the **local wall-clock
  mislabelled `Z`** (`…07:55:00Z`).
- The wrong state is stable for weeks; the right state is stable too. So once it
  settles, it stays — you just have to wait for it to settle.

## Other findings worth knowing

- **You can't fix already-uploaded items via the API.** `mediaItems.patch` only
  permits updating `description`, not the capture time. The only remedy for
  already-uploaded videos is **delete + re-upload**.
- **Test-harness gotchas** if you try to reproduce this:
  - With the current restricted scopes (`…appcreateddata`), `mediaItems.get`/`search`
    only return items **your app created**. You can't inspect arbitrary library items.
  - **Dedup:** re-uploading a file that's already in the library (e.g. a video you
    downloaded *from* Google Photos) is deduplicated to the existing item — which
    your app may not be able to read. Use freshly-distinct content for controls.
- **The diagnostic signal** is the *settled* `creationTime`: `true-instant Z`
  (honored) vs `local-wall-clock Z` (mangled). That's a far cleaner read than
  eyeballing the UI. A known-good iPhone `.mov` makes a good positive control.

## How camflow handles this

camflow remuxes camera videos to QuickTime `.mov` and adds the atom before upload,
from a temp copy so the queued original is never touched. See
`internal/lib/exif.go` (`remuxAndTagCanonVideo`). This is why `ffmpeg` is a required
dependency.

## References

- Apple `com.apple.quicktime.creationdate` (QuickTime metadata `mdta` keys).
- ISO base media file format `ftyp` major brand (`qt` vs `mp42`).
- Google Photos Library API: `mediaItems.get`, `mediaItems.patch` (description-only).
