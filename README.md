# photoscrawl

Local-first Apple Photos crawler for the OpenClaw crawl-family ecosystem.

`photoscrawl` builds a `photos.sqlite` archive from a user's Photos library. The
goal is not photo backup. The goal is to help users understand their own library:
where photos were taken, when they were taken, what is visible, which
documents/screenshots/receipts exist, which assets belong together, and what
evidence supports each result.

## Principles

- Go product code only.
- Use `github.com/openclaw/crawlkit` for shared crawler mechanics.
- Local-first by default; no cloud model calls unless the user explicitly selects
  assets or derivatives to send.
- Read-only Photos access. Never write back to Photos.
- Snapshot before crawling live library state.
- Metadata for all assets, local classification for high-signal coverage.
- Store observations and evidence, not final people/trip/place truth.

## Installation

Current builds require macOS 13 Ventura or later on Apple silicon or Intel. `photoscrawl` uses
native Objective-C/CGO bridges to PhotoKit, CoreLocation, MapKit, CoreImage,
CoreGraphics, and ImageIO, so release archives are intentionally
Darwin-only. Download the archive for your architecture from GitHub Releases,
extract `photoscrawl`, and place it on your `PATH`.

```sh
photoscrawl --version
```

There is no Homebrew formula in the debut release.

## Development

Building from source requires Go 1.27.0 or later and the macOS SDK. The preferred
build toolchain is Go 1.27.1, selected by the `toolchain` directive in `go.mod`.

The Makefile exposes the same core targets as the other OpenClaw crawler
repositories:

```sh
make help
make build
make check
make snapshot
```

CI runs `make check` on native macOS with the preferred Go toolchain and on
Linux without CGO using the declared Go 1.27.0 minimum. The portable check covers
archive logic and synthetic providers; release builds remain macOS-only.
`devenv shell verify` runs the same gates with the Nix environment pinned to Go
1.27, including its formatter.

`make snapshot` builds local GoReleaser artifacts without credentials and never
publishes them.

On macOS, `go test -v -run TestExportNativeIntegration ./cmd/photoscrawl` builds
the CLI with a required synthetic PhotoKit library. It exercises native export
cancellation, late callbacks, and filesystem failures without accessing Photos.

## Releases

Official releases run only through the manual **Release (unified)** GitHub
Actions workflow, which signs and notarizes the Darwin artifacts before
publishing them:

```sh
gh workflow run release-unified.yml --repo openclaw/photoscrawl -f version=X.Y.Z
```

`make release` refuses local publishing and prints that exact command.

## Finding and ranking photos

After `import-apple`, the archive holds the signals Photos already computed:
named faces with eye state and quality, per-photo aesthetic and blurriness
scores, screenshot subtypes, and duplicate flags. The commands below read
those signals and never change Photos.

- `find` filters by required people, date range, place, text, and media.
- `rank` orders a set best first and can keep the best few per burst, time
  gap, or day. It ranks by one quality score (aesthetic, lowest named-face
  quality, and subject focus); a missing signal takes the median of the
  photos being ranked, so photos without faces are not penalized.
- `junk` lists screenshot, blurry, eyes-closed, and duplicate candidates with
  the signal values and a suggested photo to keep instead. These are review
  candidates, not decisions. Open-eye replacements must include every named
  person in the closed-eye photo.
- `similar` finds photos sharing model terms and Apple labels with a seed,
  outside the seed's own moment; `forgotten` finds strong photos in no user
  or shared album; `sheet` renders numbered contact sheets.
- `share-check` is an advisory privacy gate: a photo passes only when its
  saved model reply includes a privacy assessment and no sensitive category.
  Unrecognized sensitivity phrases require review, even when they do not
  match a known blocked category. Only narrowly recognized absence and
  people-presence notes can pass without a category match.
  Show photos to a person before sending them anywhere.

`find`, `rank`, `junk`, `similar`, `forgotten`, and `share-check` accept
`--exclude-ids-file` with archive IDs or Photos local identifiers, so callers
can skip photos they already handled.

Quality and eye-state decisions use detected faces from the Photos database.
Search-index person labels remain searchable but do not count as additional
faces with unknown eye state.

Duplicate candidates use positive Photos group IDs or matching primary-original
resource hashes. Auxiliary thumbnails, edits, and paired resources cannot prove
that two assets are duplicates. Similarity frequencies exclude retained deleted
assets, hidden assets, and videos.
Common-feature pruning starts at 40 live, visible images, so small libraries can
still return label-overlap matches.
Explicit hidden or video seeds can find visible image matches; their labels do
not inflate the comparison corpus. Contact sheets allow up to 64 tiles per page
and 2048 pixels per tile, with a 16-megapixel (64 MiB RGBA) canvas limit. Reduce
`--per-sheet` or `--tile` when their combination exceeds that limit.
Sources above 64 megapixels produce placeholders. Dimensions are checked before
Go decoding or native rendering, and rendered output is checked before decoding.

Schema 5 archives cannot be opened by older binaries. Stop every process that
uses an archive and keep the previous binary. Before upgrading, make a
consistent SQLite backup in a private directory (replace the example paths):

```sh
umask 077
sqlite3 /path/to/photos.sqlite ".backup '/private/backup/photos-before-schema5.sqlite'"
```

Upgrade every process together, then run `photoscrawl init --db
/path/to/photos.sqlite` once. To roll back, stop every process again, copy the
backup to a **new** database filename with no existing `-wal` or `-shm` files,
and point the previous binary and all archive consumers at that copy with
`--db`. Keep the upgraded archive separately. Recovery restores the backup's
state; imports and classifications performed after the backup are not included.
There is no in-place schema downgrade.

## First Commands

```sh
go run ./cmd/photoscrawl metadata --json
go run ./cmd/photoscrawl init --json
go run ./cmd/photoscrawl status --json
go run ./cmd/photoscrawl crawl --library "$HOME/Pictures/Photos Library.photoslibrary" --json
go run ./cmd/photoscrawl import-apple --library "$HOME/Pictures/Photos Library.photoslibrary" --json
go run ./cmd/photoscrawl crawl --provider sqlite --library "/path/to/scratch.photoslibrary" --json
go run ./cmd/photoscrawl classify --limit 100 --json
go run ./cmd/photoscrawl classify --local-model gemma4:e4b --limit 20 --json
go run ./cmd/photoscrawl classify --local-model photoscrawl-qwen3-vl-8b --local-model-api openai --local-model-url http://127.0.0.1:1234/v1 --limit 20 --json
go run ./cmd/photoscrawl classify --local-model photoscrawl-qwen3-vl-8b --local-model-api openai --local-model-url http://127.0.0.1:1234/v1 --allow-icloud-downloads --limit 20 --json
go run ./cmd/photoscrawl search --query "drone beach portugal" --json
go run ./cmd/photoscrawl timeline --from 2026-05-27T00:00:00Z --to 2026-05-28T00:00:00Z --json
go run ./cmd/photoscrawl open --id asset:<id> --json
go run ./cmd/photoscrawl export --id asset:<id> --output /path/to/export --json
go run ./cmd/photoscrawl export --id asset:<id> --output /path/to/export --timeout 2m --json
go run ./cmd/photoscrawl neighbors --id asset:<id> --json
go run ./cmd/photoscrawl people --json
go run ./cmd/photoscrawl find --person "Alex" --person "Sam" --from 2026-07-01 --to 2026-07-14 --place italy --rank quality --json
go run ./cmd/photoscrawl rank --from 2026-07-12 --to 2026-07-14 --group day --per-group 5 --json
go run ./cmd/photoscrawl junk --kind screenshots --older-than 90d --exclude-ids-file hidden.json --json
go run ./cmd/photoscrawl evidence --row-id asset:<id> --json
go run ./cmd/photoscrawl place-context --input <private-eval-run>/metadata/E001.json --json
go run ./cmd/photoscrawl place-card --input <crawlkit-cache-dir>/place-context/<key>.json
go run ./cmd/photoscrawl place-backfill --json
go run ./cmd/photoscrawl eval-card --library "$HOME/Pictures/Photos Library.photoslibrary" --allow-icloud-downloads --limit 1 --models gemma4:31b-cloud --ollama-url https://ollama.com/api --json
```

`status` reports the resolved archive filename used by `init` and other archive
commands. Only a nonexistent archive is reported as missing; filesystem errors
are returned to the caller.

Default runtime paths come from crawlkit platform dirs. The primary database is
`photos.sqlite` under the crawlkit data dir; provider caches and exported
originals use the crawlkit cache dir.

Original exports wait until completion by default. `export --timeout <duration>`
opts into a time limit (`0` keeps the unlimited default). Ctrl-C or an expired
deadline cancels the native PhotoKit resource request and removes its temporary
file. A completed export atomically replaces the destination; failures preserve
an existing file. PhotoKit reads the active system library; `export` cannot target
a separate Photos library.

`crawl` tries PhotoKit first for metadata. PhotoKit enumerates the active system
Photos library; the `--library` path is validated and recorded as the requested
source. If PhotoKit is unavailable or denied, the crawler falls back to a verified
private copy of `database/Photos.sqlite` and labels that evidence as
`photos_sqlite_snapshot`.

`crawl` does not export originals or force iCloud downloads. It records already
local package media paths for derivatives/renders/originals when they exist, so
content classification can use local files without changing Photos or iCloud
state. Every imported asset is queued for `classify`.

`import-apple` makes verified, consistent copies of Apple Photos' live
databases in a private temporary directory, reads those copies, and removes
them when the import finishes. It
adds Apple's existing named-person records, search index, original camera and
EXIF details, edit state, and Photos quality scores to `photos.sqlite`:
captions, keywords, detected text, scene labels, activities, venues, dates,
places, people, camera/source clues, and photo types. Both the current
`psi.sqlite` search index and the newer `leo.sqlite` layout are supported. The
import is an authoritative refresh of only the Apple-derived observation rows;
it never writes to the Photos library or uploads the source databases.

Before writing, Apple imports share one verified private archive copy for schema
and library-binding checks. Writable archive opens still require temporary disk
space for a complete archive copy; the shared check avoids a second concurrent
copy during Apple imports.

Apple's Photos database is a private schema and can change between macOS
releases. The importer validates every required table and column before it
writes observations, so an unknown schema fails closed instead of silently
mislabeling photos.

After upgrading an existing archive to this version, run `photoscrawl init
--json` once before `status`, `search`, or other read-only commands. This
applies the required archive migration without recrawling media. If macOS
reports `operation not permitted` while opening the Photos database, grant Full
Disk Access to the installed `photoscrawl` executable in **System Settings →
Privacy & Security → Full Disk Access**, then rerun the import from the logged-in
macOS session or its background worker.

Crawls merge into the archive. An asset missing from a later enumeration stays
live; only an explicit provider deletion signal creates a tombstone. Asset
tombstones retain their reason and also tombstone archived resource rows such as
derivatives and thumbnails. A later explicit live record restores the asset and
the resources present in that record without discarding other archive history.

`classify` drains that queue into evidence-backed local metadata observations.
With `--local-model <model>`, it also sends image bytes to a local Ollama or
OpenAI-compatible vision server and stores typed candidate
observations:
scene summaries, visible-text summaries, place-type/name/venue candidates,
objects/foods, anonymous people presence, privacy hints, cluster terms, and
uncertainties. These are evidence-backed model observations, not durable
people/place/trip truth.

By default, local-model classification only reads images already stored on the
Mac. With `--allow-icloud-downloads`, missing images are requested from PhotoKit
as bounded 1600-pixel JPEG previews. Each preview is removed after its model
result is stored, so classification does not accumulate an originals archive.
A preview that PhotoKit has not delivered within two minutes fails that asset
instead of stalling the run, and cancelling a run stops an in-flight download.

Local-model endpoints must resolve entirely to loopback addresses. Redirects
are checked under the same rule. Evidence records the actual response endpoint
and that image bytes were transmitted over the loopback interface.

`neighbors` returns source-level adjacent assets only. It does not create trips,
people, places, or clusters. Current reasons are deterministic archive facts:
same burst id, same album id, same resource hash, nearby creation time, nearby
raw GPS, and shared local observation labels.

Hash-neighbor evidence refers to the specific matching resources and their
recorded hashes. Multiple resource matches retain their evidence even when
they share one neighbor reason.

`timeline` returns raw geotagged asset observations for one explicit half-open
time range. It preserves the asset and location-observation identifiers and
reports upstream horizontal accuracy when available. It does not infer stops,
routes, trips, or events.

Timeline uses the same archive schema checks as other queries and excludes
retained asset tombstones even when their classification queue rows are absent.

`place-context` enriches one asset's own latitude/longitude/accuracy/time into
address hierarchy and candidate nearby POIs. Apple's network-backed
CoreLocation reverse geocoder is the required step. MapKit POI search is
optional venue evidence: no POI found is recorded as `poi_status: "none"`,
while real provider errors still fail. Text output is a compact deterministic
place card; `--json` returns provider evidence. Apple address areas of interest
are rendered as map context, not as POIs.

Cached place evidence is shared by coordinates, accuracy, and search radius.
Each response retains the current request's asset, image, and capture time while
preserving the provider evidence's original generation time.

`place-card` renders cached provider evidence into the same deterministic
Markdown card without re-calling providers. It keeps address detail, normalizes
map context, caps useful POIs, and omits raw coordinates, warnings, provider
counts, provenance, and invented confidence. It is for eval harnesses and
private provider experiments.

`place-backfill` is a private evidence command for full-library Apple provider
probes. It reads `photos.sqlite`, dedupes exact location/accuracy keys, retries
provider failures, and writes the manifest, attempts, raw successful provider
outputs, and final errors under the crawlkit data dir's
`backfills/place-context-full/apple-ingest` subtree.

Backfill keeps coordinate/index assignments in `identities.json`, including
retired keys, so inserting, removing, or restoring locations cannot transfer or
reset retry histories. Existing runs bootstrap this ledger from `manifest.json`;
that manifest continues to list only the current keys. New keys receive unused
indexes, and retired artifacts stay on disk without contributing to current
summary counts. Coordinates and accuracy retain their stored precision.
Keep the identity ledger with the artifacts: missing or ambiguous identity data
fails before provider calls instead of guessing which location owns attempts.

An evidence write failure stops the current backfill round, cancels pending
attempts, and returns the original write error.

Ctrl-C cancels backfill retry waits promptly. If a command is blocked in a native
call or input read, a second Ctrl-C terminates it.

`eval-card` is an opt-in research harness for prompt/model evaluation. It uses
the built-in version of `prompts/photo-card-v1.md` (or an explicit `--prompt`
file), prepares canonical full-resolution JPEGs
from originals, passes full metadata as a sidecar prompt input, and writes all
private images, metadata, and model responses under the crawlkit data dir's
`evals` subtree. If `--allow-icloud-downloads` is set, PhotoKit may download
missing originals into the crawlkit cache dir's `originals` subtree. `crawl`
does not force iCloud downloads; `classify` does so only with its explicit
`--allow-icloud-downloads` flag and uses temporary bounded previews.

Preparation tries at most three times the requested card limit. Downloaded
originals are limited to 256 MiB each and 512 MiB per run, streamed into owned
temporary cache files and removed after preparation, including on failure.
Existing local originals and older cache files are never deleted by this cleanup.
The summary includes `assets_attempted`; retained JPEGs and metadata remain in
the output directory, while temporary original paths are provenance only.

The eval-card summary is written only after model evidence is saved and its
manifest flush and close succeed. Evidence write failures or cancellation stop
the run before a new summary is written; provider failures remain recorded eval
results and contribute to `model_calls_failed`.

## Current Useful Output

Today the POC sees useful source facts and optional local multimodal observations:

- asset timing, media type, dimensions, favorite/hidden state, timezone, and
  burst metadata;
- resource type, UTI, filename, local/remote availability, iCloud download need,
  and resource hash when already local;
- regular, shared, and smart album membership, album folder paths, and raw GPS
  observations with evidence refs;
- Apple Photos captions, keywords, detected text, scene labels, activities,
  venues, people, camera/source clues, and media categories imported from the
  local Photos search index;
- metadata-only observations for media type, local content availability,
  geometry, burst membership, resource UTI/type, and weak
  screenshot/document/receipt candidates from filenames, albums, and metadata;
- optional local model observations from local images or explicitly downloaded
  temporary previews, plus normalized terms for search and later clustering;
- quality observations for model failures such as prompt leakage;
- status coverage counts for GPS, observations, local resources, remote
  resources, classification queue state, and observation types;
- search/timeline/open/evidence/neighbors JSON that points every claim back to source
  rows or evidence ids.

It does not create durable identities, trips, places, relationships, embeddings,
or global clusters yet.

## Why This Shape

This is a local-first personal media index:

- typed local objects;
- provenance on every derived claim;
- entity and link resolution as explainable pipelines;
- graph traversal and timelines as first-class query shapes;
- clusters and trips as later hypotheses, not v1 truth;
- user-owned local archive with no sharing or hidden scoring by default.

Photos are useful because a saved image usually records something the user cared
about: a place, person, document, trip, purchase, home, event, hobby, meal,
screenshot, or drone flight. The crawler's job is to preserve that context
without pretending GPS, face labels, or classifier labels are perfect facts.

## v1 Scope

Build `photos.sqlite` with:

- assets and resource metadata from Apple Photos;
- local original-download queue with bounded cache/ringbuffer;
- GPS observations as raw coordinates only;
- album membership;
- file/resource hashes when originals are available;
- Vision/Core ML observations: labels, OCR, faces, barcodes, screenshot/document
  markers, quality/similarity signals where useful;
- evidence refs for every observation;
- JSON status/search/timeline/open/neighbors/evidence commands.

Out of scope for v1:

- durable person identity;
- durable trip/place/event truth;
- relationship inference;
- global photo clustering;
- cloud classification by default;
- Photos writeback.
