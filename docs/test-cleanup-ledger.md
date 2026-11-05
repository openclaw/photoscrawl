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
