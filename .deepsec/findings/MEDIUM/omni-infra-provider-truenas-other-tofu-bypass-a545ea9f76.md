# [MEDIUM] TOFU ISO hash check silently downgrades to first-use trust on property-read failure

**File:** [`internal/provisioner/steps.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/provisioner/steps.go#L236-L353) (lines 236, 237, 269, 271, 280, 330, 353)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** medium  •  **Slug:** `other-tofu-bypass`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

`expectedHash, _ := p.client.GetDatasetUserProperty(ctx, isoDataset, isoHashProperty)` (line 269, also line 236 on cache-hit) discards any error from the property read. `classifyTOFU(expectedHash, downloadedHash)` then receives `expectedHash == ""` whenever the property RPC errors transiently, which is the same input as a genuine first-time download — so `tofuMismatch` is not returned, and the code proceeds to record `downloadedHash` as the new trusted baseline (line 353). An attacker who can induce a property-query failure (overload TrueNAS, transient WebSocket error, MITM the JSON-RPC response) at the moment of an ISO re-download can rotate the TOFU baseline to bytes of their choosing, defeating the supply-chain detection that this code path was specifically built to provide. The `tofu_pinned` log field (line 280) will report `false` even though a hash was previously recorded, but no error is raised. The comment on `expectedHash` describes the intent ("future downloads … verified") but the implementation makes verification opt-out on any read error.

## Recommendation

Distinguish 'property absent' (legitimate first-time use) from 'property read failed' (transient error). Return the error from `GetDatasetUserProperty` and refuse to proceed with TOFU baseline establishment when the read fails; require the caller to retry once the property RPC is healthy. Same fix applies to the cache-hit branch where a poisoned-marker check is skipped on read error.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
