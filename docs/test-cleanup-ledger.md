# Test cleanup ledger

## Audit status

This pass covers the tracked tests and fixtures at
`24510457d20db632aa12c946213bebf5cfa80a32` (PR #126).
All 42 Go test files, the Python scanner test file, and four fixture sources
received a full read. The inventory matches the completed review.

| Scope | Go test files |
| --- | ---: |
| `cmd/photoscrawl` | 3 |
| `internal/archive` | 26 |
| `internal/evalcard` | 4 |
| `internal/photos` | 4 |
| `internal/place` | 5 |

The Python file is `.github/scripts/test_vulncheck.py`.
The fixture sources are `curation_photos.sql`, `curation_psi.sql`,
`exportdriver/main.go`, and `photokit_fixture.m` under
`cmd/photoscrawl/testdata/`.

All findings below landed. No queued finding remains from this pass.
This records test cleanup and demonstrated gaps, without claiming exhaustive
product correctness or live Photos end-to-end coverage.
The broader crawl-family audit remains separate.

## Landed changes

PRs retain the detailed fault cases, review results, and CI evidence.

| PR | Outcome |
| --- | --- |
| [#86](https://github.com/openclaw/photoscrawl/pull/86) | Remove overlapping elapsed-time cancellation coverage. Use `testing/synctest` for library and CLI retry cancellation. |
| [#89](https://github.com/openclaw/photoscrawl/pull/89) | Remove the duplicate hidden-candidate case. Replace the noise constant check with ranking and explanation checks. |
| [#90](https://github.com/openclaw/photoscrawl/pull/90) | Move the unreferenced eval driver into native tests. Exercise rendering, the embedded prompt, colliding model names, and evidence-write failure. |
| [#92](https://github.com/openclaw/photoscrawl/pull/92) | Replace a nanosecond timeout and sleep with an expired deadline. Require `context.DeadlineExceeded`. |
| [#93](https://github.com/openclaw/photoscrawl/pull/93), [#94](https://github.com/openclaw/photoscrawl/pull/94) | Fix second-interrupt ordering and retain signal cancellation causes. Remove the handshake sleep and require SIGINT termination. |
| [#95](https://github.com/openclaw/photoscrawl/pull/95) | Replace six fatal assertions in HTTP handlers with reported errors, HTTP 400 responses, and normal returns. |
| [#96](https://github.com/openclaw/photoscrawl/pull/96) | Remove repeated privacy phrase assertions already covered by the full-result table. Keep mixed and fully negated cases. |
| [#98](https://github.com/openclaw/photoscrawl/pull/98) | Report early classifier exits while waiting for preview export. Preserve gates, result counts, and revision fencing. |
| [#99](https://github.com/openclaw/photoscrawl/pull/99) | Remove an unreachable retry-header success branch. Keep real loopback failure and recovery phases. |
| [#100](https://github.com/openclaw/photoscrawl/pull/100) | Require query and timeline manifest entries before checking their read-only flags. |
| [#101](https://github.com/openclaw/photoscrawl/pull/101) | Exercise competing media classes, sizes, and paths. Require attachment of the preferred candidate. |
| [#102](https://github.com/openclaw/photoscrawl/pull/102) | Share XDG setup and complete path checks. Remove a partial duplicate case and 18 test lines. |
| [#103](https://github.com/openclaw/photoscrawl/pull/103) | Remove a foreign-schema fixture blocked by the hardlink guard. Run the identical main-hardlink case once. |
| [#104](https://github.com/openclaw/photoscrawl/pull/104) | Check indexes in populated v3/v4 upgrade cases. Remove a standalone fixture and 22 test lines. |
| [#105](https://github.com/openclaw/photoscrawl/pull/105) | Require timeline validation errors instead of accepting unrelated database failures. |
| [#106](https://github.com/openclaw/photoscrawl/pull/106) | Remove the duplicate SQLite album-exclusion case. Require the negative-gap validation error. |
| [#107](https://github.com/openclaw/photoscrawl/pull/107) | Share populated foreign-database fixtures across schema versions. Check preservation after each call and remove 32 test lines. |
| [#108](https://github.com/openclaw/photoscrawl/pull/108) | Check ordered Apple import rows and FTS mappings instead of counts. Require the injected metadata failure. |
| [#109](https://github.com/openclaw/photoscrawl/pull/109) | Check actual contact-sheet tile colors with the existing tolerant assertion. Keep metadata, dimensions, numbering, and mode checks. |
| [#112](https://github.com/openclaw/photoscrawl/pull/112) | Supply complete package roots for direct-error checks. Cover dependency errors and incomplete graphs. |
| [#113](https://github.com/openclaw/photoscrawl/pull/113) | Make the existing six-candidate POI fixture exceed the cap. Require the four nearest useful rows, categories, distances, and order. |
| [#114](https://github.com/openclaw/photoscrawl/pull/114) | Fold undated-photo and screenshot checks into the populated status case. Remove two archive setups and 15 test lines. |
| [#115](https://github.com/openclaw/photoscrawl/pull/115) | Replace partial people checks and a duplicate query with complete ordered results. Distinguish face and asset counts. |
| [#116](https://github.com/openclaw/photoscrawl/pull/116) | Check rank keys, keepers, members, order, and the retained photo after trimming in the existing fixture. |
| [#117](https://github.com/openclaw/photoscrawl/pull/117) | Fix unchecked day-duration multiplication. Cover the valid boundary and three overflow inputs through the parser and real CLI. |
| [#118](https://github.com/openclaw/photoscrawl/pull/118) | Check repeated migration in populated v3/v4 cases. Remove the empty fixture and 31 test lines. |
| [#119](https://github.com/openclaw/photoscrawl/pull/119) | Fix mismatched PSI group rowids that imported zero observations. Check imported groups and labels through real CLI search. |
| [#120](https://github.com/openclaw/photoscrawl/pull/120) | Require all three ordered same-event IDs. Remove the relative-position helper and ten test lines. |
| [#121](https://github.com/openclaw/photoscrawl/pull/121) | Retry cancelled classification at the same clock time. Require released claims, a successful preview, written observations, and completed state. |
| [#122](https://github.com/openclaw/photoscrawl/pull/122) | Reuse the asset-aware eval provider. Reduce 100 candidates to seven while retaining the six-attempt boundary and cleanup checks. |
| [#123](https://github.com/openclaw/photoscrawl/pull/123) | Make the outside-checkout prompt case send one model request. Check built-in instructions, expanded metadata, and image bytes. |
| [#124](https://github.com/openclaw/photoscrawl/pull/124) | Check literal coordinates, complete albums, retained identity, and absent location fields in the existing timeline fixture. |
| [#125](https://github.com/openclaw/photoscrawl/pull/125) | Share one list writer across 18 callers. Preserve bytes and permissions while removing 18 duplicate test lines. |
| [#126](https://github.com/openclaw/photoscrawl/pull/126) | Fold invalid-ID checks into the scoring fixture. Require specific errors and remove one archive setup and five test lines. |

## Coverage deliberately retained

- Keep portable eval preparation and native rendering as separate boundaries.
  Native tests use a real renderer, generated images, and loopback model HTTP.
  Mock image bytes test HTTP serialization rather than image decoding.
- Keep the PhotoKit fixture and export driver for native cancellation, byte
  budgets, and atomic promotion. Explicit zero and default unlimited values
  cover separate CLI inputs.
- Keep SQLite WAL, uncommitted-writer, rollback, and source-copy cases.
  Direct paths, symlinks, literal whitespace, hardlinks, and actual SQLite
  filenames cover different filesystem boundaries. Keep all nine sidecar cases.
- Keep missing, empty, read-only, view-only, versioned-empty, and foreign
  database cases. Preserve committed-WAL backup and migration recovery checks.
- Keep cross-library import replacement, empty inputs, unknown scope, and late
  rollback failures. Keep provider identity, revision, and durable-write fences.
- Keep distinct share-check assessment states, complete saved replies, and
  unknown sensitivity. Preserve model literal-host, DNS, rebinding, and redirect
  checks because they protect different network boundaries.
- Keep similarity frequency and shortlist populations, forgotten-assets
  percentile rows, and repeated duplicate-group queries that exercise map order.
  Keep crawl delta, selective-write, tombstone, and restore transitions.
- Keep direct manifest flush/close failures separate from run-level write
  propagation and summary ordering. Keep subprocess signal handshakes and
  bounded readiness observations.

## Proof and limits

Changes use synthetic temporary SQLite data, files, media, and loopback servers.
They do not use live Photos data or external model services.
No release or deployment forms part of this audit.

Relevant changes include paired before/after fault checks that distinguish
new failure detection from retained guards. Temporary overlays leave tracked
production bytes unchanged. The PRs record individual outcomes.
Local checks include `make check` and scoped race suites.
Independent review and native/portable CI accompany the landed changes.

The native eval and CLI import/search cases exercise real integrations with
synthetic inputs. Local media selection covers filesystem behavior rather than
native image decoding. This pass does not establish live-library, iCloud,
external-provider, or full installed-app end-to-end behavior.
