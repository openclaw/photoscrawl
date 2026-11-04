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
