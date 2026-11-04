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
