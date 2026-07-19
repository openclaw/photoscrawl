# Changelog

## Unreleased

- Add explicit asset and resource tombstones, lossless schema migration, and merge-only crawl updates that never infer deletion from a missing snapshot row.
- Add loopback-only OpenAI-compatible local vision classification for LM Studio, with thanks to @mbelinky.
- Align the module path with `openclaw/photoscrawl`, add CrawlKit control metadata for launcher discovery, use current dependencies, and prefer MapKit reverse geocoding on macOS 26.
- Update CrawlKit to v0.14.2, modernc SQLite to v1.54.0, and Go to 1.26.5.
