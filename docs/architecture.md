# photoscrawl Architecture

## Decision

Build `photoscrawl` as a standalone OpenClaw/crawlkit Go crawler. It owns the
Apple Photos schema, local classification policy, privacy policy, and query
surface. `crawlkit` owns reusable mechanics only.

## Source Strategy

Use the safest source available for each job:

1. PhotoKit for supported asset, collection, resource, location, and metadata
   access.
2. A read-only snapshot for library database inspection where PhotoKit does not
   expose useful internal analysis.
3. Apple's existing Photos analysis as evidence when it is extractable and good.
4. Local Vision/Core ML classification to fill gaps or improve signal.
5. Local multimodal models for higher-signal image understanding when the user
   opts into local content classification.

Do not make private Photos SQLite tables the only path. Treat them as adapters
with schema-version checks and evidence labels.

## Ingestion Model

The crawler has two stages:

- `crawl`: enumerate assets and cheap metadata for all assets.
- `classify`: add metadata observations and optionally classify local images or
  opt-in PhotoKit previews through a resumable queue. Video content classification
  is future work.

`crawl` may record paths to files that already exist inside the Photos library
package, such as derivatives, renders, or originals. It must not export media,
write to Photos, or trigger iCloud downloads.

The opt-in `eval-card --allow-icloud-downloads` flow downloads
originals. It prepares at most three times the requested sample size and limits
new originals to 256 MiB each and 512 MiB per run, removing owned temporary
originals after preparation. It builds one local media index per run and reuses
it for snapshot metadata and original selection.

`classify --local-model MODEL --allow-icloud-downloads` downloads temporary
PhotoKit previews at a default maximum dimension of 1600 pixels. Each invocation
owns its preview directory and removes it after classification. Downloads have
a two-minute timeout; cancellation leaves the queue row available for retry.
The explicit `export` command can also download a selected original through
PhotoKit to the requested output directory.

## Classification Roadmap

Current classification combines metadata rules with optional local multimodal
observations. Vision/Core ML OCR, barcode detection and face embeddings below
are future capabilities, not claims about the current implementation.

Classify for signal, not uniform checklist compliance.

Always consider:

- scene/object labels;
- OCR;
- face count and boxes;
- barcode/QR detection;
- screenshot/document/receipt markers;
- image quality and visual similarity.
- local multimodal summaries, candidates, privacy hints, uncertainty notes, and
  clustering terms.

But store observations only when they have useful confidence/evidence. A cat
photo does not need barcode output; a receipt/screenshot/document probably does
need OCR; a drone-looking burst probably needs camera/device/resource metadata
and location precision.

Local multimodal output is candidate evidence. It belongs in generic model
observation rows with the model id, prompt version, evidence ref, and normalized
terms. Promotion into trips, places, people, relationships, or durable events
belongs in a later user-reviewed layer.

## Location Policy

Store raw GPS observations first. Reverse geocoding is a separate derived layer.

Reason: GPS can be off by enough to imply the wrong business or home. A raw
coordinate is evidence; "barber shop" versus "pizza place" is a fallible
derived claim and must carry method, confidence, and evidence.

## Identity Policy

Use Apple's People/faces data if available, but label it as source evidence.
Future local face detection/embedding can fill gaps in user annotations.

Do not create canonical people in v1. Store anonymous face observations, Apple
person labels, and candidate links. Promotion to people belongs in a later
user-reviewed identity layer.

## Query Model

The first query layer is object/evidence traversal:

- `status`: archive health and counts.
- `search`: FTS over assets and observations.
- `timeline`: raw geotagged asset observations in an explicit time range.
- `open`: asset/resource/observation detail with evidence.
- `neighbors`: albums, locations, faces, same resource hash, same burst/live
  photo, similar image, nearby time/place candidates.
- `evidence`: why a row or edge exists.

Neighbors are source-level adjacency, not truth. Each returned neighbor must
name the method and evidence ids behind the link. v1 neighbor reasons are
limited to deterministic archive facts such as same album id, same burst id,
same resource hash, nearby timestamp, nearby raw GPS, or shared local observation
labels.

Higher concepts like trips, recurring places, drone flights, or named places are
later hypotheses built from these facts.
