# [MEDIUM] Unauthenticated /healthz triggers backend TrueNAS RPC on every request with no rate limiting

**File:** [`internal/health/health.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/health/health.go#L39-L70) (lines 39, 40, 42, 45, 66, 70)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** medium  •  **Slug:** `rate-limit-bypass`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

`handleHealthz` calls `s.checker(ctx)` on every inbound request — the checker is the actual TrueNAS connectivity probe (WebSocket round-trip), not a cached state. There is no authentication, no rate limiting, and `/healthz` and `/readyz` are both wired to the same handler. The author already acknowledges in the response-body comment that this endpoint can leak through a misconfigured Service or Ingress and sanitises the error string accordingly; the same misconfiguration also gives an external attacker an amplification primitive — each cheap HTTP request causes a JSON-RPC round-trip against TrueNAS, so a sustained flood can saturate the WebSocket connection or crowd out legitimate provisioner RPCs. Also missing: `WriteTimeout` and `IdleTimeout` on the server, leaving open the slow-response variant of the same DoS.

## Recommendation

Either (a) cache the checker result with a short TTL (e.g., 1–5s) so probe storms collapse to a single backend call, or (b) make the checker non-blocking — return the cached result that a separate goroutine refreshes on a fixed interval. Add `WriteTimeout` and `IdleTimeout` to the http.Server. Document that the listener should be bound to the pod-internal interface only, not exposed via Service/Ingress.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-22)
