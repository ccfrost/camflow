# Google Photos drops the capture timezone on videos uploaded via the Library API

If you upload videos to Google Photos through the **Library API** and they show up
at the **wrong time** — shifted by your UTC offset, often onto the previous day —
this explains why, what does *not* fix it, and what does. Photos (JPEG) are
unaffected; this is video-only, and in practice it only bites **camera-recorded
MP4s**, not phone videos.

Keywords (so this is findable): google photos api video wrong time / wrong
timezone / off by hours / previous day, `mediaMetadata.creationTime`, MP4 vs MOV,
`com.apple.quicktime.creationdate`, `.MP4` vs `.mov` filename extension, ftyp
brand `mp42` vs `qt`.

## TL;DR

- **Cause:** Google honors a video's timezone offset only when the **filename it
  receives ends in `.mov`**. Upload the same bytes named `.MP4` — what Canon and
  many cameras write — and it mis-parses the timestamp, landing the displayed time
  off by the offset. The offset lives in the `com.apple.quicktime.creationdate`
  atom; whether Google trusts it is decided by the **filename extension**, *not* by
  the file's `ftyp` container brand and *not* by the upload MIME type (both proven
  irrelevant — see [Which signal is it](#which-signal-is-it-the-filename-extension)).
- **Fix:** make sure the file carries a `com.apple.quicktime.creationdate` atom
  with the offset, and **upload it under a `.mov` filename**. That's it — you do
  **not** need to transcode or even remux; an untouched Canon MP4 simply *renamed*
  `.mov` is honored. (Earlier guidance here said you must remux MP4 → QuickTime
  `.mov`; that does work, but the container change it makes is incidental — the
  `.mov` name is doing the work. See below.)
- **Biggest trap:** the API's `creationTime` and the web UI show a **transient**
  value right after upload that *settles* to the (correct or wrong) value 30–60+
  minutes later. Checking too early gives a false reading. **Only trust the settled
  value, >1 hour after upload.**

## Worked example

A Canon clip shot at **2026-06-06 07:55:00 −08:00** (true instant
`2026-06-06 15:55:00Z`):

| | what Google stores as `creationTime` | what the UI shows |
|---|---|---|
| **Correct** (uploaded as `.mov`) | `2026-06-06T15:55:00Z` | Jun 6, 7:55 AM |
| **The bug** (uploaded as `.MP4`) | `2026-06-06T07:55:00Z` (local wall-clock mislabelled as `Z`) | Jun **5**, 11:55 PM |

Google takes the local wall-clock from the atom and treats it as if it were UTC,
then the UI re-applies the offset — so the time ends up wrong by (roughly) the
offset, here landing on the previous day.

## Why only videos (and only camera videos)

- **Photos work** because Google reads the offset from JPEG EXIF
  (`OffsetTimeOriginal`). For **video** it ignores EXIF and reads the
  QuickTime/MP4 atoms instead.
- The standard MP4 date atoms (`mvhd`, `tkhd`, `mdhd`) are UTC-only by spec — no
  timezone field. Apple added `com.apple.quicktime.creationdate` (in `moov/meta`,
  handler `mdta`) to carry an explicit offset like `2026-06-06T07:55:00-08:00`, and
  Google honors it — **but only when the file is presented with a `.mov` filename.**
- **Phone videos are already `.mov`**, so they display correctly and nobody
  notices. The bug only surfaces for camera MP4s (`.MP4`), which is why it's
  underreported.

## What does NOT fix it

We tested each of these and checked the **settled** `creationTime` hours later. All
still mangle — because they leave the upload **named `.MP4`** and only change
metadata or the container:

| Attempt | uploaded as | settled `creationTime` | result |
|---|---|---|---|
| Add the `com.apple.quicktime.creationdate` atom (offset correct) | `.MP4` | `…07:55:00Z` | ❌ wrong |
| Atom **+ rewrite `mvhd`/track dates to local time** (mimic iPhone) | `.MP4` | `…07:55:00Z` | ❌ wrong |
| Atom **+ strip all EXIF / maker-note / GPS** metadata | `.MP4` | `…07:55:00Z` | ❌ wrong |
| Atom **+ losslessly remux to a QuickTime (`qt`-brand) container**, still named `.MP4` | `.MP4` | `…07:55:00Z` | ❌ wrong |

The first three were long read as "metadata can't fix it." The fourth row is the
telling one: even a genuine QuickTime-brand container is mangled if it's **named
`.MP4`**. So the lever was never the metadata *or* the container brand — it was the
filename extension all along, and every failed attempt above happened to keep the
`.MP4` name.

Notably, the atom + local-`mvhd` and the fully-stripped files looked **correct for
the first ~50 minutes**, then drifted to the wrong value — see the verification trap
below. Do not be fooled.

(Also: do **not** end up with two `meta` blocks in `moov` — e.g. from an ffmpeg
trim that added an empty one with `-movflags use_metadata_tags`— Google picks the
empty one and ignores your atom entirely. A clean single-pass edit avoids this.)

## What DOES fix it

Carry the offset in the atom and **upload under a `.mov` filename**:

| Attempt | uploaded as | settled `creationTime` | result |
|---|---|---|---|
| Canon MP4 (brand `mp42`) **merely renamed `.mov`** + atom | `.mov` | `…15:55:00Z` | ✅ correct |
| Same content remuxed to a `qt`-brand container + atom, named `.mov` | `.mov` | `…15:55:00Z` | ✅ correct |
| A genuine iPhone `.mov` (positive control) | `.mov` | true instant | ✅ correct |

The first row is the key one: **no remux, no transcode** — the same `mp42` Canon
bytes, just renamed, are honored. The remux in row 2 also works, but it's doing
more than necessary; the `.mov` extension is what Google keys on.

Minimal recipe (no ffmpeg):

```sh
# Write the creationdate atom (local wall-clock + offset) onto a .mov-named copy,
# and set the mvhd create/modify dates to the true UTC instant. QuickTimeUTC=0
# makes exiftool store these literally, without its own UTC shift. (Editing a copy
# keeps your original untouched.)
cp in.MP4 out.mov
exiftool -api QuickTimeUTC=0 -overwrite_original \
  '-Keys:CreationDate=2026:06:06 07:55:00-08:00' \
  '-QuickTime:CreateDate=2026:06:06 15:55:00' \
  '-QuickTime:ModifyDate=2026:06:06 15:55:00' \
  out.mov
```

The offset values come from the camera's own EXIF (`DateTimeOriginal` +
`OffsetTimeOriginal`); the UTC instant is just `DateTimeOriginal` converted by the
offset. **Upload `out.mov`** (the `.mov` name must reach Google — via the upload
file name and/or `simpleMediaItem.fileName`). Google now displays the correct local
time.

### Which signal is it: the filename extension

The fix differs from the broken upload in **three** ways that all move together
when you "remux to `.mov`":

- the **filename extension** (`.mov` vs `.MP4`),
- the **`ftyp` container brand** (`qt` vs `mp42`), and
- the **upload `Content-Type`** (`video/quicktime` vs `video/mp4`).

We isolated which one Google keys on with a full **2³ factorial** — all eight
combinations of (extension × brand × MIME), each a byte-distinct upload, read at the
**settled** value:

- **Filename extension: 4-of-4 honored as `.mov`, 0-of-4 as `.MP4`** — the lever.
- `ftyp` brand (`qt` vs `mp42`): 2-of-4 each way — **no effect**.
- Upload MIME (`video/quicktime` vs `video/mp4`): 2-of-4 each way — **no effect**.

The two decisive cells: an `mp42`-brand Canon file *renamed* `.mov` and uploaded as
`video/mp4` was **honored**; a genuine `qt`-brand remux *named* `.MP4` was
**mangled**. So the `.mov` extension is **necessary and sufficient**; brand and MIME
do nothing. (Tested `.mov` vs `.MP4` specifically; we did not separately probe case
variants or whether Google reads the extension from the upload header vs the
`fileName` field — we set both to the same value.)

## How to verify — and the trap that fools everyone

**Google reports a transient timestamp right after upload, then settles to a
different one 30–60+ minutes later.** A correct-looking value at 5 minutes means
nothing. This is the single biggest time-sink here — it produced false "it works"
verdicts more than once during this investigation.

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
    your app may not be able to read. Dedup is byte-hash based, so any metadata edit
    (a different atom timestamp) yields a distinct item; use distinct content for
    controls.
- **The diagnostic signal** is the *settled* `creationTime`: `true-instant Z`
  (honored) vs `local-wall-clock Z` (mangled). That's a far cleaner read than
  eyeballing the UI. A known-good iPhone `.mov` makes a good positive control.

## How camflow handles this

camflow tags camera videos with the `com.apple.quicktime.creationdate` atom and
uploads them under a `.mov` filename, from a temp copy so the queued original is
never touched. See `internal/lib/exif.go` (`copyAndTagCanonVideo`).

It does this with a plain byte copy to a `<stem>.mov` temp plus an `exiftool` tag —
**no remux or transcode.** Earlier versions losslessly remuxed the MP4 to a
QuickTime (`qt`) container with `ffmpeg`, on the belief that the container brand was
the lever; the factorial above showed it isn't (the `.mov` filename is), so the
`ffmpeg` step — and the hard `ffmpeg` dependency and the "stream must be
`.mov`-muxable" failure mode — was removed. The rename-only path was validated
end-to-end: an unmodified `mp42` Canon file copied to `.mov` and tagged settles to
the correct instant and plays normally (Google processes it `READY` with full
dimensions/fps).

## References

- Apple `com.apple.quicktime.creationdate` (QuickTime metadata `mdta` keys).
- ISO base media file format `ftyp` major brand (`qt` vs `mp42`) — relevant to the
  format, but *not* the signal Google keys on (the filename extension is).
- Google Photos Library API: `mediaItems.get`, `mediaItems.patch` (description-only).
