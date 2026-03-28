# Security & Bug Issues

## Security

- [x] **S1** `repoURL` not validated — user-supplied URL passed directly into git commands via string concatenation. A crafted URL could inject git flags or redirect to an attacker-controlled host. Must validate scheme + host before use.

- [x] **S2** `cloneRepo` error leaks token — `fmt.Errorf("%s: %w", out, err)` may include the authenticated URL (with embedded token) in git's combined output. Must sanitize before propagating.

- [x] **S3** `List` silently discards port errors — `d.Ports, _ = s.getPorts(d.ID)` swallows DB errors with no logging. Silent failures hide real problems.

- [x] **S4** `storage_opt` applied unconditionally — when `StorageLimitGb == 0`, the limit clamps to 512 MB minimum and is still written to the override. Hosts without overlay2 quota support will fail to start containers. Should skip `storage_opt` when `StorageLimitGb == 0`.

- [x] **S5** Webhook endpoint enumerates deployment IDs — `GET /webhooks/github/{deploymentId}` returns distinct responses for valid vs invalid IDs, allowing enumeration. Low severity given UUID entropy, but worth fixing.

## Bugs

- [x] **B1** `List` returns `null` instead of `[]` when no deployments — `deployments` var is `nil`; zero-row query returns JSON `null`. Should initialize to empty slice.

- [x] **B2** Webhook redeploy uses stale deployment snapshot — `d` is fetched once at request time; the goroutine fires later with potentially stale config (env, resource limits). Goroutine should re-fetch before applying.

- [x] **B3** `getHeadSHA` has no context — uses `exec.Command` instead of `exec.CommandContext`, so a hung git process blocks the goroutine indefinitely with no timeout.
