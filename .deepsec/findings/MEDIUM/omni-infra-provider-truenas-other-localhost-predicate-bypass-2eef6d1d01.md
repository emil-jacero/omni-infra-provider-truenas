# [MEDIUM] isLocalOmniEndpoint IPv4 boundary bypass via userinfo or DNS subdomain

**File:** [`cmd/omni-infra-provider-truenas/main.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/cmd/omni-infra-provider-truenas/main.go#L107-L138) (lines 107, 130, 136, 138)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** high  •  **Slug:** `other-localhost-predicate-bypass`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

isLocalOmniEndpoint is documented as hostname-boundary-aware (https://localhost-hijacker.example must not match as local). The boundary check works for the localhost / [::1] forms (next char must be :, /, ?, #, or end-of-string), but the 127. branch has a digit-fallback at L136 that accepts any digit-led suffix. This makes two URLs falsely match as local: (a) http://127.0.0.1@evil.com — Go's net/url parses 127.0.0.1 as userinfo and evil.com as the actual host, so the connection goes to attacker-controlled DNS while the predicate says local; (b) http://127.0.0.1.evil.com — a wildcard / subdomain A record under evil.com resolves to attacker IP while the predicate still returns true on the leading 127. octets. Impact: bypasses the PROVIDER_ID-required gate at L237-243 that exists to prevent two SaaS tenants from sharing the singleton annotation keyspace under the default ProviderID="truenas". The author's own comment (L240-241) calls out that a shared keyspace cross-leaks tenant identity via LeaseHeldError.OtherInstanceID. Trigger requires operator misconfig of OMNI_ENDPOINT, so not externally exploitable, but it weakens a safety check the author deliberately wrote to be boundary-aware.

## Recommendation

Replace the prefix-and-digit heuristic with net/url parsing: parse OMNI_ENDPOINT, extract Hostname(), then test hostname == "localhost" || hostname == "::1" || (parsed-IP != nil && parsedIP.IsLoopback()). Reject userinfo presence outright when checking localness. This both fixes the bypass and rejects http://localhost@evil.com / http://127.0.0.1.evil.com unambiguously.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-28)
