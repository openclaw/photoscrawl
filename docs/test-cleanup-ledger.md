# Test cleanup ledger

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
