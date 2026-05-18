# [MEDIUM] Bearer API key leaks to attacker-controlled host on HTTP 3xx

**File:** [`scripts/verify-api-key-roles/main.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/scripts/verify-api-key-roles/main.go#L385-L388) (lines 385, 386, 387, 388)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** high  •  **Slug:** `other-bearer-token-leak-on-redirect`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

uploadFile constructs an http.Client with no CheckRedirect, so Go's default behavior follows up to 10 redirects and re-sends headers (including `Authorization: Bearer <TRUENAS_API_KEY>`) to the redirect target. A compromised, misconfigured, or MITM'd TrueNAS host (note: InsecureSkipVerify=true on L386 disables cert validation) can return `302 Location: https://attacker.tld/` and harvest the operator's full TrueNAS API key. The production code in internal/client/ws.go explicitly defends against this with `CheckRedirect: func(...) error { return http.ErrUseLastResponse }` (L285-287) and even has a comment explaining why — this probe tool drops that defense. Operators are encouraged to run this against real TrueNAS hosts with real keys, so the leak is exploitable in the documented use case.

## Recommendation

Add `CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }` to the http.Client, mirroring internal/client/ws.go uploadClient.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-16)
