# Photo Card Eval Protocol

This protocol evaluates whether a vision model can turn a user's own Apple
Photos images into useful photo cards. It is for local, opt-in experiments. Real
images, metadata, OCR, barcodes, model outputs, and reports stay outside the
repo.

## Goal

Pick one model and one prompt that produce high-quality photo cards:

- accurate one-line summary;
- rich visual prose that says what is actually in the image;
- useful location context without overclaiming;
- complete OCR/document extraction when visible;
- clear uncertainty;
- no raw metadata dumps or repo-leaking private paths.

## Input

Run the harness against a real Photos library:

```sh
photoscrawl eval-card \
  --library "$HOME/Pictures/Photos Library.photoslibrary" \
  --allow-icloud-downloads \
  --limit 15 \
  --models gemma4:31b-cloud \
  --json
```

The harness uses originals only. It first looks for a local original inside the
Photos package. If `--allow-icloud-downloads` is set and the original is not
local, it asks PhotoKit to download/export the original into a private cache
under the crawlkit cache dir's `originals` subtree. Normal crawl behavior
remains read-only/local-first and does not force iCloud downloads.

Preparation attempts are capped at three times `--limit`. New originals are
streamed with a 256 MiB per-object limit and a 512 MiB run allowance, then removed
from their owned temporary cache directory after preparation, even on failure.
Existing Photos package originals and older cache files are not removed.
`assets_attempted` reports the attempted sample size. Original paths marked
`photokit_original_export_temporary` are provenance, not retained artifacts.

For each asset, the harness writes a canonical full-resolution JPEG to the
private eval directory. The JPEG has display-upright pixels and does not copy the
original EXIF block. Full asset, resource, and ImageIO metadata are written as a
separate private JSON sidecar and passed to the prompt.

## Output

Default run output is:

```text
<crawlkit-data-dir>/evals/<run-id>/
  images/E001.jpg
  metadata/E001.json
  raw/E001__ollama__<model>__photo-card-v1.json
  manifest.jsonl
  summary.json
```

Nothing in that directory is commit-safe.

Tracked repo artifacts are only:

- prompt text in `prompts/`;
- protocol docs in `docs/evals/`;
- Go harness code;
- tests that use synthetic provider stubs and do not touch Photos.

## Scoring

Review outputs manually against these questions:

- Did the one-line summary identify the main point of the image?
- Did the description include the concrete visual details a human would care
  about?
- Did the model use visible text, document fields, and OCR instead of ignoring
  them?
- Did metadata improve the answer without making the model echo raw filenames,
  UUIDs, paths, or EXIF?
- Did location context stay useful and calibrated?
- Did the model invent a venue, airport, monument, route, person, or event?
- Did the card avoid tags, PR-review evidence sections, and generic usefulness
  commentary?

The current baseline prompt is `prompts/photo-card-v1.md`. New prompt iterations
should be added as separate prompt files and compared with the same image set.
