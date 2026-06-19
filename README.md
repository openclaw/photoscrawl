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

## First Commands

```sh
go run ./cmd/photoscrawl metadata --json
go run ./cmd/photoscrawl init --json
go run ./cmd/photoscrawl status --json
go run ./cmd/photoscrawl crawl --library "$HOME/Pictures/Photos Library.photoslibrary" --json
go run ./cmd/photoscrawl classify --limit 100 --json
go run ./cmd/photoscrawl classify --local-model gemma4:e4b --limit 20 --json
go run ./cmd/photoscrawl search --query "drone beach portugal" --json
go run ./cmd/photoscrawl open --id asset:<id> --json
go run ./cmd/photoscrawl neighbors --id asset:<id> --json
go run ./cmd/photoscrawl evidence --row-id asset:<id> --json
go run ./cmd/photoscrawl place-context --input <private-eval-run>/metadata/E001.json --json
go run ./cmd/photoscrawl place-card --input <crawlkit-cache-dir>/place-context/<key>.json
go run ./cmd/photoscrawl place-backfill --json
go run ./cmd/photoscrawl eval-card --library "$HOME/Pictures/Photos Library.photoslibrary" --allow-icloud-downloads --limit 1 --models gemma4:31b-cloud --ollama-url https://ollama.com/api --json
```

Default runtime paths come from crawlkit platform dirs. The primary database is
`photos.sqlite` under the crawlkit data dir; provider caches and exported
originals use the crawlkit cache dir.

Planned crawl-family commands:

```sh
photoscrawl export --format lifecrawler --json
```

`crawl` tries PhotoKit first for metadata. PhotoKit enumerates the active system
Photos library; the `--library` path is validated and recorded as the requested
source. If PhotoKit is unavailable or denied, the POC falls back to a read-only
`database/Photos.sqlite` transaction and labels that evidence as
`photos_sqlite_snapshot`.

`crawl` does not export originals or force iCloud downloads. It records already
local package media paths for derivatives/renders/originals when they exist, so
content classification can use local files without changing Photos or iCloud
state. Every imported asset is queued for `classify`.

`classify` drains that queue into evidence-backed local metadata observations.
With `--local-model <ollama-model>`, it also sends already-local image bytes to a
local Ollama vision model and stores typed candidate observations:
scene summaries, visible-text summaries, place-type/name/venue candidates,
objects/foods, anonymous people presence, privacy hints, cluster terms, and
uncertainties. These are evidence-backed model observations, not durable
people/place/trip truth.

`neighbors` returns source-level adjacent assets only. It does not create trips,
people, places, or clusters. Current reasons are deterministic archive facts:
same burst id, same album id, same resource hash, nearby creation time, nearby
raw GPS, and shared local observation labels.

`place-context` enriches one asset's own latitude/longitude/accuracy/time into
address hierarchy and candidate nearby POIs. Apple's network-backed
CoreLocation reverse geocoder is the required step. MapKit POI search is
optional venue evidence: no POI found is recorded as `poi_status: "none"`,
while real provider errors still fail. Text output is a compact deterministic
place card; `--json` returns provider evidence. Apple address areas of interest
are rendered as map context, not as POIs.

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

`eval-card` is an opt-in research harness for prompt/model evaluation. It uses
the tracked prompt files in `prompts/`, prepares canonical full-resolution JPEGs
from originals, passes full metadata as a sidecar prompt input, and writes all
private images, metadata, and model responses under the crawlkit data dir's
`evals` subtree. If `--allow-icloud-downloads` is set, PhotoKit may download
missing originals into the crawlkit cache dir's `originals` subtree; normal
crawl/classify commands do not force iCloud downloads.

## Current Useful Output

Today the POC sees useful source facts and optional local multimodal observations:

- asset timing, media type, dimensions, favorite/hidden state, timezone, and
  burst metadata;
- resource type, UTI, filename, local/remote availability, iCloud download need,
  and resource hash when already local;
- album membership and raw GPS observations with evidence refs;
- metadata-only observations for media type, local content availability,
  geometry, burst membership, resource UTI/type, and weak
  screenshot/document/receipt candidates from filenames, albums, and metadata;
- optional local model observations from already-local image derivatives or
  originals, plus normalized terms for search and later clustering;
- quality observations for model failures such as prompt leakage;
- status coverage counts for GPS, observations, local resources, remote
  resources, classification queue state, and observation types;
- search/open/evidence/neighbors JSON that points every claim back to source
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
- JSON status/search/open/neighbors/evidence commands.

Out of scope for v1:

- durable person identity;
- durable trip/place/event truth;
- relationship inference;
- global photo clustering;
- cloud classification by default;
- Photos writeback.
