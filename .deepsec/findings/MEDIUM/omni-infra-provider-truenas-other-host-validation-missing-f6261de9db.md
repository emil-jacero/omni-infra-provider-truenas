# [MEDIUM] TRUENAS_HOST not validated before being interpolated into Bearer-token URL

**File:** [`scripts/verify-api-key-roles/main.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/scripts/verify-api-key-roles/main.go#L351) (lines 351)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** medium  •  **Slug:** `other-host-validation-missing`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

uploadURL is built via `fmt.Sprintf("https://%s/_upload/", p.host)` where p.host is the raw TRUENAS_HOST env var. The production code centralizes a `validateHost()` helper (internal/client/ws.go L66-112) that rejects scheme/path/userinfo/query/fragment characters precisely because hand-formatted URLs with unvalidated hosts have caused Bearer-token smuggling bugs (their own comment at ws.go L823-826: 'fmt.Sprintf + unvalidated host = bearer exfil. Never again.'). This probe duplicates the bug. If the operator types TRUENAS_HOST=`good.tld@evil.tld` or `evil.tld/x`, the resulting URL targets an unintended host and the API key in the Bearer header is sent there. WebSocket dial uses url.URL{} struct (safer) but the upload path regresses.

## Recommendation

Either reuse the validateHost logic from internal/client/ws.go, or build the URL via `(&url.URL{Scheme: "https", Host: p.host, Path: "/_upload/"}).String()` like ws.go L827.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-16)
