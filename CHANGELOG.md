# Changelog

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
