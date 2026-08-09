# photoscrawl Architecture

Date: 2026-05-28

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
- `classify`: process image/video content through a resumable queue.

`crawl` may record paths to files that already exist inside the Photos library
package, such as derivatives, renders, or originals. It must not export media,
write to Photos, or trigger iCloud downloads.

Originals may be downloaded for classification, but downloads must be bounded:

- keep a local cache budget;
- process batches;
- evict originals/thumbnails after observations and hashes are recorded;
- resume from cursor state;
- record `needs_download` when the original is not local.

CPU is allowed. Disk blowups are not.

## Classification Policy

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
Also run local face detection/embedding where useful because user annotations are
sparse and biased toward important people.

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
