# Changelog

## Unreleased

## 0.3.1 - 2026-09-05

- Update SQLite to v1.58.0 and prefer Go 1.27.1 for source builds; Go 1.27.0 and macOS 13 remain supported.

## 0.3.0 - 2026-09-01

### Highlights

- Import original camera and EXIF details, Photos edit state, and Apple quality scores as evidence-backed searchable observations.

## 0.2.0 - 2026-09-01

### Highlights

- Import named people and Apple Photos search-index labels into evidence-backed, locally searchable observations.
- Preserve regular, smart, and shared album membership, including nested folder paths and shared-photo source identity.
- Add bounded geotagged timelines, optional unlocated-photo results, and original PhotoKit export for images and videos.

### Data integrity

- Snapshot live Photos and search-index SQLite databases with WAL consistency checks before reading them.
- Refresh Apple-derived observations atomically, fail closed on unknown schemas, and preserve existing archive data when preflight checks fail.
- Keep classified assets out of the crawl queue and retry interrupted metadata tagging.

### Reliability and tooling

- Stop place-backfill promptly on evidence write failures without deadlocking workers or starting the remaining attempts; thanks @SebTardif.
- Cancel place-backfill retry waits on Ctrl-C while preserving second-interrupt termination for blocked commands; thanks @SebTardif.
- Return eval-card manifest flush and close failures before writing its summary; thanks @SebTardif.
- Update to Go 1.26.7, CrawlKit v0.14.7, SQLite v1.57.0, deadcode v0.49.0, and the v7 checkout/setup-go actions; retain CodeQL's supported Go toolchain.
- Update Go dependencies, including `go-isatty` v0.0.24 and `x/sys` v0.47.0.
- Require macOS 13 Ventura or later with the update to Go 1.27.0 and CrawlKit v0.14.8.
- Standardize the Makefile's build, check, snapshot, and fail-closed release targets across the crawler repositories.
- Publish the project under the MIT License.

## 0.1.0 - 2026-07-18

### Highlights

- Debut a local-first, read-only Apple Photos crawler that builds an evidence-backed SQLite archive from PhotoKit metadata and a snapshot-safe Photos database fallback.
- Add metadata classification, evidence-linked search and neighbors, Apple place context, and opt-in local multimodal classification through loopback-only Ollama and OpenAI-compatible endpoints, with thanks to @mbelinky.
- Add a private photo-card evaluation harness with bounded original caching, canonical image rendering, tracked prompts, and explicit iCloud-download consent.

### Data integrity

- Preserve explicit asset and resource deletions as durable tombstones through lossless schema migration and merge-only crawls; missing snapshot rows never imply deletion.
- Keep runtime paths on current CrawlKit platform directories and reject unrelated or newer archive schemas without mutating them.

### Release engineering

- Ship Darwin amd64 and arm64 archives from a native macOS GoReleaser build because PhotoKit, CoreLocation, MapKit, CoreImage, CoreGraphics, and ImageIO require Objective-C/CGO framework bridges.
- Add tag-stamped version reporting and the unified Foundation-signed, notarized release pipeline.
- Update CrawlKit to v0.14.3, modernc SQLite to v1.54.0, and Go to 1.26.5.
