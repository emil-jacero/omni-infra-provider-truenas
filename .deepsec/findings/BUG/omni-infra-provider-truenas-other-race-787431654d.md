# [BUG] lastErr field written but never used in response — dead state with lock contention

**File:** [`internal/health/health.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/health/health.go#L24-L77) (lines 24, 72, 73, 77)
**Project:** omni-infra-provider-truenas
**Severity:** BUG  •  **Confidence:** high  •  **Slug:** `other-race`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

`s.lastErr = err` is written under `s.mu.Lock()` on every request, but no code path ever reads `lastErr`. The `lastOK` timestamp is read for the response, but `lastErr` is dead. The lock acquire on every healthz call is harmless under low load but unnecessary, and the unused field is misleading to future maintainers (it suggests a 'last error reason' surface that doesn't exist).

## Recommendation

Either expose `lastErr` in the response (with sanitisation), or remove the field and the write — the lock can be downgraded to RLock-only for `lastOK` reads after a separate atomic update path.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-22)
