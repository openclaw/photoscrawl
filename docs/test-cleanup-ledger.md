# Test cleanup ledger

## Native evaluation integration

- Moved the unreferenced `testdata/evaldriver` program into the macOS test suite.
  Its assertions now run under `go test` instead of requiring an undocumented
  manual command.
- Kept the real native renderer, generated PNG, synthetic provider, and loopback
  model server. Cover the embedded prompt outside the checkout, distinct retained
  results for colliding model names, and failure to persist model evidence.
- Kept mocked model tests as portable unit coverage of their separate branches.

No live Photos library, private media, or external model service is used.
No production behavior or release artifacts changed.

## Backfill cancellation

- Removed the elapsed-time dispatch cancellation test. The existing
  `TestRunBackfillJobsCancelsBlockedDispatchAndAttempts` waits for every worker
  to start before cancelling, and covers blocked dispatch and active attempts.
- Changed library and CLI retry cancellation tests to use `testing/synctest`.
  They wait for the synthetic archive to reach retry backoff, cancel the context,
  and require `context.Canceled` without advancing the virtual clock.
- Kept temporary SQLite fixtures and subprocess signal tests. These cover
  archive integration and process shutdown boundaries separately.

This is a bounded cancellation-test cleanup, not a complete repository audit.
No production behavior or release artifacts changed.

## Similarity ranking and exclusions

- Removed the standalone hidden-candidate test. The scoring-and-exclusions test
  already exercises the same fixture, options, and hidden-asset rejection.
- Replaced the noise-policy constant assertion with actual ranking coverage.
  The existing food fixture now shares every zero-weight term with the phone
  candidate, and requires exactly the expected candidates and the single
  `cell_phone` explanation. This also detects common-noise-only candidates.
- Kept the frequency-cutoff boundary and small-library matching tests.

This is a bounded similarity-test cleanup. Synthetic temporary SQLite data only;
no production behavior or release artifacts changed.

## Expired original-export deadline

- Replaced the nanosecond timeout and sleep with an already-expired deadline.
  The fixture starts canceled without depending on timer scheduling.
- Require `context.DeadlineExceeded`, rather than accepting any error from
  original export. Keep the prompt-return bound and separate cancellation and
  in-flight preview-export tests.
- The expired context returns before PhotoKit; fixtures use temporary paths.

No native export behavior, Photos data, or release artifacts changed.

## Second-interrupt ordering

- Removed the fixed sleep between cancellation acknowledgement and the second
  interrupt in the subprocess test. It now sends that interrupt immediately
  and requires SIGINT termination, rather than accepting any failed exit.
- Fixed the ordering the sleep masked: unregister the command's signal channel
  before canceling its context. Callers observing cancellation can immediately
  interrupt again, including while native work or stdin remains blocked.
- Preserve the signal-specific cancellation cause returned by eval and place
  operations, and assert it in the child handshake.
- Keep the child's readiness/cancellation handshake and bounded parent context.
  Only the test-owned child receives signals; no Photos archive is opened.

## Model HTTP fixture failures

- Replaced six fatal assertions in the Ollama and OpenAI-compatible handlers
  with non-fatal test errors, HTTP 400 responses, and normal returns. Fatal
  assertions ran in server goroutines and aborted requests with EOF.
- Keep every request check, successful synthetic model response, persisted
  observation assertion, and root-test fatal assertion.

No model behavior, private media, live Photos library, or release changes.

## Repeated privacy phrase assertions

Removed the standalone every-occurrence test: its two exact phrases already
exist in the 62-row table with equal or stricter full-result comparisons. Keep
the mixed-negation passport and fully negated cases, fixtures and production code.

Temporary first-occurrence and negation-bypass mutations fail those retained
rows. Full local checks, archive race tests, review and CI use synthetic data.

## Concurrent classifier startup failures

- Let both concurrent-classifier tests observe the first classifier's result
  while waiting for preview export. An early return now reports its error
  instead of leaving the test blocked until the global suite timeout.
- Preserve the exporter gates, explicit release ordering, result counts,
  recrawl revision fencing, and synthetic Photos library fixtures.
- Temporary Go overlays cancel only the background classifier before it starts:
  both original tests time out; both fixed tests fail promptly with the original
  cancellation error. Full local checks, archive race tests, independent review,
  and CI validate the unchanged success paths. No live Photos data or releases.

## Model retry fixture phases

- Remove the unreachable `X-Retry-Proof` success branch from the content-retry
  server. No request sets that header; the test switches to its success handler
  explicitly after observing the initial failure.
- Keep the real loopback HTTP failure/recovery phases, synthetic image, archive
  and classifier calls. Before/after fault overlays must still reject a server
  that succeeds immediately or never recovers. Run scoped race tests, local
  checks, independent review and platform CI. No production or release changes.

## Manifest command presence

- Require query and timeline command entries before checking their non-mutating
  flags. The old zero-value map lookups accepted either missing command.
- Ten before/after race probes pass their expected outcomes: healthy manifests
  pass, repaired checks reject both omissions, and mutation-flag guards still
  reject both changed flags. Keep identity, paths, capability and privacy checks.
- Validate full local checks, archive race tests, built-CLI metadata, independent
  review and platform CI. No new fixtures, production changes, Photos data,
  external model calls or releases.

## Local media candidate selection

- Replace the lone derivative fixture with five competing temporary files.
  Require derivative/render/original ordering, then size and path ordering,
  and attachment of the preferred candidate. Keep availability and metadata
  checks and the separate synthetic-resource test through the public wrapper.
- Ten before/after race probes pass their expected outcomes: healthy suites
  pass; reversed class, size or path ordering and last-candidate attachment
  pass the old tests but fail the expanded fixture. Production source is restored.
- This is filesystem integration coverage, not native image-decoding or Photos
  end-to-end proof. No production changes, private media, live library access,
  external model calls or releases. Full local checks, review and platform CI
  are required before landing.

## Platform path fixtures

- Share XDG setup and a complete `Paths` comparison between absent/present
  legacy-archive cases. Remove the standalone partial legacy test and 18 test
  lines; keep both inputs and the distinct derived-runtime-directory test.
- Sixteen valid before/after race probes preserve database and ordinary-share
  failure detection and now reject lost config/data/cache/log/share paths when
  a legacy archive exists. Healthy cases pass; production source is restored.
- Run full local checks, independent review and platform CI before landing.
  Temporary directories only; no production changes, live archives or releases.

## Preflight fixture overlap

- Remove the foreign-source test whose hardlink fixture stops schema inspection.
  Keep exact hardlink refusal and direct foreign DELETE/WAL preservation tests.
- Run the main-hardlink fixture once instead of once per unused sidecar suffix;
  retain all nine distinct sidecar alias/orphan cases and their assertions.
- Eight before/after race probes preserve rejection of hardlink-guard,
  preflight-schema-guard and both-schema-guard bypasses; healthy suites pass.
  The removed test passes both schema-bypass probes. Production is restored.
- Require full local checks, independent review and platform CI before landing.
  Synthetic temporary SQLite/filesystem fixtures only; no live data or releases.

## Migration index fixture reuse

- Fold the standalone index test into populated upgrade/recovery rows for both
  v3 and v4. Keep their distinct face-column layouts, index creation, current
  version and restored-backup checks, with 22 fewer test lines.
- Eight before/after race probes preserve missing-index and wrong-version
  detection. A v4-only face-label overwrite passes the old empty v4 fixture but
  fails the new populated row. Healthy rows pass; production is restored.
- Require full local checks, independent review and platform CI before landing.
  Synthetic temporary SQLite data only; no live archives or releases.

## Timeline validation errors

- Require the expected validation-error prefix for all four invalid-bound cases.
  The empty database path can fail independently, so accepting any error hid
  missing validation. Keep the existing inputs and avoid new archive fixtures.
- Eight before/after race probes: healthy tests pass; bypassing the from, to or
  ordering guard passes the old test and fails the repaired test. Production
  source is restored byte-for-byte after each probe set.
- Require full local checks, independent review and platform CI before landing.
  No production changes, live Photos data, external model calls or releases.

## Forgotten-assets test overlap

- Remove the SQLite album-exclusion test: the exact-result test uses the same
  fixture and rejects both album members. Keep both provider kinds, mapping
  cases, percentile fixtures, date filters and unknown-date coverage.
- Require the negative-gap validation error instead of accepting a database
  failure. Eight before/after race probes retain detection of either missing
  SQLite album filter and now reject a missing gap guard. Healthy tests pass;
  production source is restored byte-for-byte.
- Require full local checks, independent review and platform CI before landing.
  Temporary synthetic data only; no production changes or releases.

## Foreign database fixture reuse

- Share the populated foreign-database fixture between older/current schema
  versions, removing 32 test lines. Keep both Search and Init calls and check
  the sentinel, absence of the asset table and original version after each call.
- Eight before/after race probes retain guard-bypass and sentinel-corruption
  detection. A current-version-only overwrite passes the old test and fails the
  shared fixture. Healthy rows pass; production source is restored byte-for-byte.
- Keep missing, empty, read-only, view-only and versioned-empty input coverage.
  Require full local checks, review and platform CI. Temporary synthetic SQLite
  data only; no production changes, live archives or releases.

## Apple import preservation snapshots

- Replace per-table counts with ordered row contents for the same six import
  tables and per-asset FTS mappings. Keep both libraries, empty/unknown-library
  cases, ambiguity checks and the real SQLite metadata-failure trigger.
- Require the injected metadata error instead of accepting any failure.
  Twelve before/after race suites retain row-loss detection and now reject
  cross-library content changes, content corruption after failure, lost FTS
  mappings and the wrong error cause. Healthy suites pass; source is restored.
- Reuse the existing SQL row reader and fixtures. Require full local checks,
  independent review and platform CI. Synthetic archive integration only;
  no production changes, live Photos access, model calls or releases.

## Contact-sheet rendered contents

- Check tile colors in the existing two-page JPEG fixture, alongside source
  metadata, numbering, dimensions and private file modes. Reuse the existing
  tolerant color assertion; keep cancellation, orientation and size-bound tests.
- Eight before/after race suites show that blank tiles, source images replaced
  with placeholders and blank placeholders pass the old test and fail the
  repaired test. Healthy suites pass; production source is restored byte-for-byte.
- Require full local checks, independent review and platform CI before landing.
  Synthetic media only; no new fixture files, production changes or releases.

## Complete package-load rejection fixtures

- Replace the malformed error-only package fixture with a complete loaded root
  carrying its direct error; cover dependency errors and incomplete graphs too.
- Keep empty-root, version-mismatch and loaded-versus-selected checks, plus
  advisory download retry tests.
- Eight before/after full verifier-suite runs retain direct-error detection and
  now reject two guard regressions accepted by the old suite. Healthy cases pass.
  Faults exist only in memory; production bytes remain unchanged.
- Require evaluator tests, local checks, independent review and native/portable
  CI. Synthetic package metadata only; no product changes or releases.

## Place-card POI cap and ordering

- Replace three filtered-out store rows with useful POIs in the existing
  six-candidate fixture. Keep one closer store to exercise noise filtering.
- Require the exact four nearest useful POIs, including category, distance and
  order, alongside the existing address and provider-count checks.
- Ten before/after race checks show that caps of three or five and reversed
  distance ordering pass the old test and fail the repaired test. Lost useful-POI
  filtering still fails both tests; healthy tests pass. Faults use temporary Go
  overlays; production bytes remain unchanged.
- Require full local checks, independent review and native/portable CI before
  landing. Synthetic inline data only; no new fixtures, product changes or
  releases.

## Share-check status fixture reuse

- Fold the standalone undated-photo and SQLite-screenshot tests into the
  existing status/count/exclusion test. Keep both assets and their explicit
  status checks, including the screenshot reason, with 15 fewer test lines and
  two fewer complete archive fixture builds.
- Keep latest/malformed/missing assessments, saved full replies, unknown
  sensitivity and argument-validation tests as separate cases.
- Ten paired before/after race suites preserve detection of SQLite screenshot
  misclassification, rejection of undated photos, ignored exclusions and lost
  notes. Healthy suites pass. Temporary Go overlays leave production unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI before landing. Synthetic SQLite data only; no production
  changes, live Photos access or releases.

## People result fixture and duplicate query

- Replace partial people checks and a second identical tombstone query with one
  literal ordered comparison of labels, UUIDs, kinds, face counts and asset counts.
- Keep the tombstoned person fixture. Add a second detection in an existing image
  so face counts and distinct asset counts differ; no extra asset or fixture file.
- Twelve paired before/after curation race runs show that lost UUIDs, missing
  distinct-asset counting, a wrong person count and an unexpected person pass the
  old test but fail the repaired test. Tombstone leakage still fails both.
  Healthy cases pass; temporary Go overlays leave production bytes unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. Synthetic SQLite data only; no product changes, live
  Photos access or releases.

## Curation rank group results

- Replace time/day group-count-only checks with literal ordered keys, keepers
  and member IDs in the existing rank case. Require the correct retained photo
  after per-group trimming; reuse the same archive, IDs and local-midnight data.
- Fourteen paired before/after curation race runs show that wrong members,
  keys, keepers, group order and trimmed photos pass the old checks but fail
  the repaired checks. Both suites still reject a lost time-gap boundary.
  Healthy suites pass; temporary Go overlays leave production unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. No new tests, helpers or fixtures, production changes,
  live Photos access or releases.

## Screenshot age duration overflow

- The day parser multiplied unchecked integers into nanoseconds. `106752d`
  wrapped negative, moving the screenshot cutoff into the future; other large
  inputs wrapped positive and were also silently accepted.
- Bound day counts before multiplication. Extend the existing duration test
  with zero, the default, the largest valid whole-day count, and three overflow
  cases. Reuse the existing synthetic CLI import fixture to require a failure
  and the invalid-day diagnostic for an overflowing screenshot age.
- Before the fix, all three overflow cases were accepted and the CLI returned
  screenshot candidates successfully. Require full local checks, archive race
  tests, independent review and native/portable CI. No new fixtures, live Photos
  access or releases.

## Populated migration idempotence

- Fold the empty face-column idempotence test into the existing populated v3/v4
  upgrade cases. Check schema version, retained rows, face values, query index
  and all six face columns after both the upgrade and repeated migration.
  Keep committed-WAL backup and recovery checks unchanged.
- Remove one empty database fixture and 31 test lines. No new helpers, fixtures
  or production changes.
- Twelve paired before/after race runs preserve rejection of repeat-migration
  errors, missing columns and missing indexes. Repeat-only face corruption or
  evidence deletion passes the old suite and fails the consolidated suite.
  Healthy suites pass; temporary Go overlays leave production unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. Synthetic data only; no live Photos access or releases.

## CLI search-index fixture links

- The PSI fixture linked group IDs 10–12 to implicit group rowids 1–3, so the
  real CLI imported zero search-index observations while its curation checks
  passed. Use explicit matching rowids on the same three fixture rows.
- Require three resolved groups, known/unknown categories and a resolved person
  observation. Search the imported label through the CLI and check its asset,
  observation identity, source, category and title. Keep existing source-byte
  preservation and eyes-closed checks. No new tests, helpers or fixture files.
- Fourteen paired before/after race runs build and invoke the real CLI through
  temporary Go overlays. Decoder no-op, missing search FTS, wrong label category,
  dropped unknown group and leaked search-person eye signals pass the old test
  and fail the repaired test. Both still reject lost detected eye state.
  Healthy suites pass; source files remain unchanged after the proof.
- Require full local checks, independent review and native/portable CI. Synthetic
  SQLite data only; no production changes, live Photos access or releases.

## Similar same-event result completeness

- Replace relative position checks with the existing exact-ID comparison for
  the three ordered same-event results. Missing candidates previously counted
  as being last and could pass. Remove the unused index helper.
- Keep the same synthetic archive and frequency-threshold population. No new
  tests, helpers, fixtures or production changes; remove ten test lines.
- Twelve paired before/after race runs use temporary Go overlays. Missing
  same-day/phone results, reversed trailing order and a duplicate result pass
  the old check and fail the replacement. Both reject a wrong first result.
  Healthy cases pass; source bytes stay unchanged and overlays are removed.
- Require full local checks, archive race tests, independent review and
  native/portable CI. No live Photos access or releases.

## Cancelled classification retry

- The cancellation case checked only that the queue row was not failed; its
  loopback model server was never used. Reuse the same archive, server and
  options to retry the photo at the identical clock time after cancellation.
- Require the original pending state and ingestion reason, one successful
  preview/classification with written observations, and the persisted completed
  state and model-observation reason. No new tests, helpers or fixture files.
- Twelve paired before/after race runs use temporary Go overlays. Unreleased
  claims, prematurely completed rows, overwritten reasons and a failed retry
  write pass the old test and fail the replacement. Both still reject loss of
  cancellation identity. Healthy cases pass; source bytes stay unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. Synthetic SQLite/media and a loopback server only; no
  production changes, live Photos access or releases.

## Eval provider and attempt fixture

- Reuse one asset-aware provider stub for empty, budget and model eval cases;
  remove the duplicate provider type and six test lines.
- Reduce 100 identical candidates to seven: the six-attempt limit plus one.
  Keep both runs and the empty-cache checks after failed preparation.
- Ten paired before/after race runs preserve rejection of caps of five/seven,
  a removed attempt cap and lost original cleanup. Healthy cases pass.
  Temporary overlays leave source bytes unchanged.
- Require full local checks, eval race tests, independent review and
  native/portable CI. Synthetic data only; no product changes or releases.

## Built-in eval prompt request

- The outside-checkout prompt case had no assets or models, so it sent no
  request. Reuse the prepared-image helper and require one loopback request
  carrying the built-in instructions, expanded asset metadata and image bytes.
- Keep execution outside the checkout. No new tests, helpers or fixture files.
- Twelve paired before/after race runs show that empty prompt text, absent
  metadata, empty image payload and a skipped model call pass the old case
  and fail the repaired one. Reading the prompt from the checkout fails both.
  Healthy cases pass; temporary overlays leave source bytes unchanged.
- Require full local checks, eval race tests, independent review and
  native/portable CI. Synthetic data only; no product changes or releases.

## Timeline result fields

- Require literal latitude/longitude and complete album records in the existing
  timeline fixture. Check both result lengths, retained located identity, and
  all absent location fields on the existing unlocated photo.
- Twelve paired before/after race runs show that swapped coordinates, lost album
  kinds, fabricated longitude and fabricated accuracy/precision pass the old
  case and fail the replacement. Both still reject an included upper time bound.
  Healthy cases pass; temporary overlays leave source bytes unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. No new tests, helpers, fixtures, product changes or releases.

## Shared list fixture writer

- Replace the sheet-specific list writer and join helper with the existing
  writer, renamed `writeTestList`. Update its 18 callers across sheet,
  curation, share-check and similarity cases; remove 18 duplicate test lines.
- Preserve empty-file output, trailing newlines, argument order, paths and
  `0600` permissions. No test assertions, fixtures or production code change.
- Require full local checks, archive race tests, independent review and
  native/portable CI. Synthetic data only; no releases.

## Similarity ID error fixture

- Move the missing/unknown ID checks into the populated scoring case. Remove
  one repeated archive setup, the standalone case and five test lines.
- Require the specific required-ID and missing-asset errors; keep all scoring,
  reason and exclusion assertions.
- Ten paired before/after race runs show unrelated errors pass the old checks
  and fail the replacement. Accepting invalid IDs still fails both. Healthy
  cases pass; temporary overlays leave source bytes unchanged.
- Require full local checks, archive race tests, independent review and
  native/portable CI. No new helpers, fixtures, product changes or releases.
