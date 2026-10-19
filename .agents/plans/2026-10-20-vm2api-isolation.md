# Per-account VM2API isolation — approved design and implementation plan

User approval: 2026-10-20, “每账号独立容器+数据目录” and “可以没问题”.

Goal: imported GPT accounts automatically receive separate VM2API control-plane containers and persistent data, with monitoring inside New API visible only to administrators/root. Ordinary site login never provisions a container.

## Global constraints
- Preserve existing working account imports and shared gateways until each migration is verified.
- One managed instance and dedicated gateway/channel per account; never silently fall back to a shared pool after isolation is requested.
- Only admin/root and existing account ownership checks may access monitoring/actions. Credentials stay encrypted server-side and never enter UI/logs.
- No Docker socket in the New API application. A restricted local Unix-socket manager controls fixed image, mounts, limits and naming; no user-supplied command, image or path.
- Complete tests with real SQLite, MySQL and PostgreSQL when database behavior changes. Test fresh/upgrade migration twice.
- Fetch/integrate origin/main before external builds; verify ancestry and recheck remote before service switch. Production loads complete amd64 artifacts and never compiles or builds (including runtime slot image fallback).
- Resource capacity is enforced; no overcommit or disabling host load guards. Each instance has independent admin/API/database secrets and config/data/bin/share/vms directories; host kernel/network remains shared.
- No new documentation in docs/ or plugins/. This approved spec is recorded here to respect repository policy.

## Runtime contract
Manager delivered as pre-validated Python stdlib artifact plus systemd unit. Unix socket /run/new-api-vm2api/manager.sock, mode 0600, mounted into application. Root-owned config pins VM2API image by digest, prebuilt slot images, kin-computer path, instance root, listening private host IP, port range and capacity. Registry files mode 0600, atomic persistence, process-level flock for mutations, bounded Docker commands.

Routes: PUT /v1/instances/{id} with empty JSON ensures the instance; GET returns sanitized state; POST /v1/instances/{id}/{start,stop,restart,rebuild} changes lifecycle; DELETE cleans managed child and control containers before data deletion. ID is exactly 32 lowercase hex derived from account SecretID hash. Repeated ensure never generates new credentials or ports. Response fields: id, status, container_name, data_directory, image, port, management_url, relay_url, updated_at, error_code. PUT additionally returns management_key and relay_key (same VM2API master key initially); GET/actions never return keys. Errors use fixed error_code, never subprocess output.

Container vm2api-account-{id} uses host network, HOST=<configured private bridge gateway IP>, PORT=<reserved port>, per-instance bind mounts, unique VM2API_CONTAINER_NAME/VM2API_HOST_ROOT, Docker socket required by upstream, NET_ADMIN/NET_RAW as required by upstream. Kin-computer copied from approved binary. Per-instance Docker CLI wrapper rejects build/buildx/compose/buildkit commands so upstream cannot build on production. Prebuilt guest images must exist before starting.

Slot id for managed instances: new-api-{id}-isolated, to avoid old shared slot/container collision. Lifecycle cleanup only touches this derived child name and verifies its bind mount belongs to this instance root before removal. Rebuild keeps data. Capacity exhaustion returns explicit retryable status; no shared fallback.

## Backend task
Files: model/gpt_account_vm2api_instance.go; model/main.go registration; service/gpt_account_vm2api_instance.go; existing service/gpt_account_gateway.go; controller/gpt_account_vm2api.go; router/gpt-account-router.go; controller/gpt_account.go; existing controller/gpt_account_gateway_test.go.
- Extend schema with instance record unique account ID and unique instance ID, gateway binding, template/source gateway, state/error/timestamp; no plaintext secrets. Add managed-account marker to gateways or equivalent unambiguous ownership field. Do not offer managed gateways as templates or permit account/gateway CRUD to reassign another account's instance.
- Enabled by server env VM2API_INSTANCE_MANAGER_SOCKET and VM2API_INSTANCE_HOST (fixed private IP). Private HTTP exception ONLY for persisted managed gateway matching configured host and reserved port, never general arbitrary gateway URLs.
- Persist verified OAuth first using current checkpoint/lease discipline; provisioning/sync runs as retryable asynchronous task. One job lease, durable progress, revalidate ownership/generation at credential use and channel publication.
- Ensure dedicated disabled channel and gateway, atomically bind the account, then import into deterministic isolated slot; enable only after successful sync. Do not expose pending account through shared channel. Failure keeps recoverable state.
- Existing shared accounts migrate via explicit POST /:id/vm2api/instance (same admin/owner authorization). Export package server-to-server, pause old slot before credential transfer, change only package slot ID for new namespace, verify remote identity and readiness, then switch channel/binding. Retain original gateway and state for rollback; do not copy entire shared DB or log secrets. If upstream package migration cannot be safely implemented, report blocker before enabling migration.
- GET /:id/vm2api/instance returns sanitized instance metadata even if upstream offline. POST /:id/vm2api/instance/{start,stop,restart,rebuild} guarded lifecycle. Existing VM/status/log APIs use dedicated binding; whitelist fields returned, validate log vm_id, never pass arbitrary upstream secrets.
- Account deletion obtains lease/fences jobs, disables routing, cleans manager instance first, then deletes bound DB records/channel. Failures retain pending_cleanup record/account; no deletion of shared resources.
- Tests first: two accounts distinct gateway/channel/container roots; repeated ensure recovery; concurrent jobs; immutable managed association; revoked owner; ordinary user denial; upstream offline; delete failure/retry; sanitized responses; old import tests preserved.

## Manager task
Files: bin/vm2api-instance-manager.py; bin/vm2api-instance-manager_test.py; bin/vm2api-instance-manager.service.
- Implement exact contract above; unittest fixtures use actual temp directories and Unix HTTP socket with controlled Docker process adapter, not live credentials. Test distinct identity/ports/keys/roots, restart persistence, capacity, unexpected containers/mounts, invalid IDs/actions/payloads, redaction, cleanup ordering, lock serialization.
- Validate python3 bin/vm2api-instance-manager_test.py; python3 -m py_compile outside production. Smoke with actual Docker on isolated external environment before deploy.

## UI task
Files: web/src/features/gpt-accounts/components/vm2api-panel.tsx, index.tsx, api.ts, types.ts; existing account tests and seven locale files. Read web/AGENTS.md and shadcn/i18n/React skills first.
- Dedicated always-visible VM2API tab inside GPT Accounts for admin/root, with account selector even when selected upstream offline, and helpful empty state.
- Show account-specific slot, instance isolation state, container/version/health, quota, scheduling, request logs including actual TTFT/duration units, success/error details.
- Lifecycle buttons backed by the above endpoints, confirmed destructive/rebuild actions; success invalidates exact account query; errors visible, read-only users cannot invoke management.
- Shared account marked shared, explicit migrate action; never claim container isolation from distinct slot IDs alone. Keep management keys out of browser.
- Tests cover account switch during loading/error and mutation, permissions, empty/pending/failed/shared states, lifecycle calls and returned timing fields. Complete seven locale translations; focused tests, lint/typecheck/build.

## Release task
Review complete diff and fix findings. Sync current origin/main, test real DB matrix and front/backend, build amd64 outside production, version by commit, push branch to fork. Transfer image/artifacts, backup compose+DB+shared VM2API state, install manager/socket mount and fixed env. Verify health before migration, migrate one existing account, verify exact isolated slot+channel request logs, then second account. Recheck latest remote before switching. Preserve prior images and shared instance for rollback; do not claim latency improved solely from isolation.

## Progress
- [x] Approved design and correct integration worktree identified; clean initial HEAD ee513c81c.
- [x] origin/main fetched/integrated (already ancestor) at 32e587406c6ebe891f18ab9aeb2f4a1f6b7b9e14.
- [x] Manager implementation: 14 tests; independent review clean; real amd64 Docker control + archlinux slot create, resource/image checks, stop/start/restart/rebuild persistence and deletion passed on local Colima.
- [x] Backend isolation and migration: SQLite 3.50.4, MySQL 8.4.11, PostgreSQL 16.15 with separate log DB, fresh and upgrade migration twice; focused controller/service/model/router regressions and go build passed. Review recovery and scheduling findings fixed.
- [x] UI entry, monitoring, lifecycle and seven-language translations: 156 tests, scoped lint, typecheck, production build passed; independent final review clear.
- [x] Review, external amd64 build, branch push, production rollout and two-account verification.

Release capacity: production 4 vCPU / 8 GB; initially two dedicated instances, control 0.5 CPU + 512 MiB and guest 1 CPU + 512 MiB each. Preserve existing pause policies and disable the emptied shared channel only after verifying both migrations.


## Production rollout completed — 2026-10-20

- Build source `4d3bf7d41717c92d636888194886c3a01dfb5156`, clean at build time, contains freshly fetched `origin/main` baseline `32e587406c6ebe891f18ab9aeb2f4a1f6b7b9e14` (verified with `git merge-base --is-ancestor`). Remote baseline rechecked unchanged immediately before service switch. This later rollout-record commit changes documentation only; the deployed source remains `4d3bf7d41`.
- External linux/amd64 image `new-api:vm2-isolation-4d3bf7d41`, image ID `sha256:c5beedff8a454ea09503cffc988f3dc5178730ea11356b07f38ad6251fe4b1c5`; production loaded the complete image without building. Container healthy, manager systemd service active, public `/api/status` reports `vm2-isolation-4d3bf7d41`.
- Account 44: instance `7605e22100d78958d6a62d52d0f2c8cd`, gateway 2, channel 90, private port 19300, ready/running and schedulable.
- Account 46: instance `7ef7cd5021c25bc96a0f93eb2ec2e9a5`, gateway 3, channel 91, private port 19301, ready/running with prior paused scheduling preserved; channel remains automatically disabled while paused.
- Distinct control containers, guest containers, data directories, API keys and database secrets verified. Both access/refresh credentials recognized; secret values were not printed. Original shared slots remain stopped and unschedulable. Empty shared channel 86 manually disabled, zero enabled abilities, action audit verified. Source gateway and state retained.
- Bounded streaming `gpt-6-luna` request through New API to dedicated channel 90: HTTP 200, `response.completed`, first text 13.301 s, total 13.371 s. Direct VM2API request earlier: first text 6.399 s, total 6.447 s. These are different individual requests, not a latency comparison or evidence of improvement. Account 44 logs show the successful request on its exact isolated slot; account 46 has no test request because it is paused. Logs return only their own slot.
- Dashboard entry is **GPT Accounts → VM2API**, admin/root only. Automated UI permission and interaction tests passed. Authenticated visual production verification remains unavailable because deployment invalidated the browser session; no claim of visually verifying the signed-in dashboard is made.
- Capacity deliberately limited to two instances on the existing 4 CPU / 8 GB host, currently fully allocated. Further VM2API imports fail visibly/retryably at capacity and never fall back to the shared pool. Review host resources before raising capacity.
- Private access uses UFW rule on `br-2b35eb5de590`, source `172.18.0.2`, destination `172.18.0.1`, TCP `19300:19301`. If the New API container IP/network changes, update this narrow rule; management ports must not be opened publicly.
- Backup directory `/opt/new-api/backups/vm2-isolation-20261019-175019` contains prior compose, MySQL dump, online SQLite backup and shared VM2API state. Previous image `new-api:vm2api-c76cc8951` retained. Credential rollback requires stopping the destination and transferring its current package back; simply resuming the old source can use stale rotated OAuth credentials.
- Temporary migration helper was externally compiled and used existing service methods with fixed account/owner preconditions and verified audit events. Removed runtime helper from the application after migration. No production source compilation occurred.

### Verification record

- `python3 bin/vm2api-instance-manager_test.py`: 14 passed; external Docker smoke covered pinned amd64 control/guest images, resource limits, start/stop/restart/rebuild persistence and deletion.
- `go test ./controller ./service ./model ./router -run 'GPTAccount|VM2API' -count=1`: passed; `go build ./...`: passed.
- `python3 /private/tmp/vm2-isolation-db-matrix.py`: real SQLite 3.50.4, MySQL 8.4.11 and PostgreSQL 16.15 passed with separate log DBs. Matrix uses `go test ./controller -run '^(TestVM2API|TestGPTAccountGateway|TestImportGPTAccountOAuthWithVM2API)' -count=1 -v`, exercising fresh and representative previous-schema upgrade migrations repeatedly, data preservation and uniqueness. MySQL 5.7 and PostgreSQL 9.6 were not separately run; no minimum-version-specific SQL introduced.
- Frontend: 156 tests in four focused files, scoped lint, full typecheck and production build passed. Seven locales updated through project translation workflow. Independent reviews found no remaining Critical/Important issues.
- Applicable security references: OWASP Authentication, Session Management and OAuth2 Cheat Sheets; ASVS 5.0.0 authorization 8.2.1–8.2.3, 8.3.1–8.3.2, 8.4.1 and logging 16.2.5, 16.5.1–16.5.3. Verified server-side ownership/admin checks, revocation/lease fences, encrypted reusable secrets and redacted outputs with access-denial, ambiguous-import/replay and concurrency regressions. This is change-scoped verification, not application-wide certification.
- Production verification scripts in the release directory confirmed current image, running instances, distinct secrets, resource limits and account-specific request logs. Public status verified from the local workstation; server-origin Python request received HTTP 403 at the public edge, while internal service and local public checks succeeded.


## Reliability follow-up — 2026-10-20

Production diagnosis confirmed intermittent provider `server_is_overloaded` (503) and `server_error` (500) on account 44. Its successful requests generally waited 6–13 seconds before text. No OOM/restarts or host resource exhaustion were observed. Account 46's separate `no_codex_vm` failures are automatic probes of an intentionally paused slot, not failures of the active account. The screenshot's old shared channel 86 is manually disabled; manual channel tests still accept a disabled channel and report whole-request duration rather than TTFT.

Upstream `origin/main` was freshly fetched and integrated at `819ee2a7c40b49fe81ebc3823e20373350a20545` (VM2API 1.3.199), then patched at `9e5cd3cb2dbfa6930c99c5bc7c0b7bb248fcceb2`. Latest remote was rechecked unchanged after build and before each account switch. The patch is preserved as `bin/vm2api-codex-retry.patch` with its baseline, source commit and checksum in `bin/vm2api-reliability-release.json`; `git apply --check --reverse` against the build source passed. New API origin/main also remained `32e587406c6ebe891f18ab9aeb2f4a1f6b7b9e14` and is an ancestor of the integration branch.

- Fix explicitly recognizes `server_is_overloaded`, bounds nested transport/provider attempts with one budget, respects case-insensitive seconds/date Retry-After (over 10 seconds returns without internal retry), honors cancellation during and after waits, and never replays after downstream output. Ordinary retries preserve model, request body, reasoning and session. True attempt count is reported.
- Both isolated instances now run external-built linux/amd64 image `vm2api:reliability-9e5cd3c`, ID `sha256:d2ef59e976e4c8065ca7af68e0defffc6b843d3047595c105d75f195b09ab9e0`. Full image archive SHA256 `b1a3753ce1fe7244e6caf05ca6489e0ba502aa7df128e9c586bc948f961ea9c5` matched before/after transfer. Production only loaded and ran the artifact.
- Effective per-instance failover policy: same-account retries 1, account switches 0, total attempts 2, base delay 500 ms. These are VM2API limits; New API's pre-existing global `RetryTimes=20` was not modified and is a separate retry layer. No shared-instance fallback or model substitution was introduced.
- Latest Codex kernel SHA verified in both running containers: `0ffec538a57afbe66f13832513ee4c2a27ecc970faa9367c7cb8a13551196dd3`. Approved identity binary and pinned guest OS images preserved. Existing production guest containers were restarted, not recreated; the full-new-guest directory mount was verified externally, and no claim is made that old guest worker single-file mounts were migrated in production.
- Found and repaired a lifecycle socket bug during rollout: systemd removed/recreated RuntimeDirectory on manager restart, while the application bind mount retained the old directory inode (host 89294 versus container 85637). Added `RuntimeDirectoryPreserve=yes`, validated with `systemd-analyze verify`, reloaded systemd and restarted the existing New API container once to restore the mount. A subsequent real manager restart preserved inode 89294 on both sides and management calls worked. New API application image remains `new-api:vm2-isolation-4d3bf7d41`; it was not rebuilt or changed for this runtime patch.
- Upgraded paused account 46 first, then account 44. Both are ready/running with recognized access/refresh credentials; 44 schedulable and 46 paused as before. First immediate readiness reads during startup failed and were explicitly reconciled after health verification; final readiness was verified, not assumed from Docker acceptance. Each stop/recovery was audited through existing service methods.
- Cold backups are `/opt/new-api/backups/vm2-reliability-9e5cd3c/{44,46}.tar.gz`, plus previous manager config/service unit. Old images remain available. Any rollback must stop the destination first and preserve its current credentials; old backup tokens may have rotated.

Validation actually run:

- Agent's four targeted Codex test files: 93/93 passed, including red-to-green transient, cancellation, Retry-After, stream commitment, body preservation and nested-budget regressions. Independent review found no blocking issue.
- Root rerun: `node --test test/unit/handle-codex.test.mjs test/unit/handle-codex-reasoning-history.test.mjs test/unit/codex-slot-pool.test.mjs test/unit/handle-codex-washed.test.mjs`: 82/82 passed. `go test ./...` in both `worker/` and `api-kernel/`: passed. Full upstream Dockerfile built frontend, helpers and payload outside production.
- `python3 /private/tmp/vm2-reliability-smoke.py sha256:d2ef59e976e4c8065ca7af68e0defffc6b843d3047595c105d75f195b09ab9e0`: passed on local Colima. Real control/API-kernel and tokenless Codex health, real tokenless create/start, fixed guest image, resource/security limits, latest directory mount, SQLite integrity/sentinel and keys persisted across restart/rebuild; own test containers/data cleaned. First run exposed only a temporary assertion's CAP_ prefix normalization issue, fixed without changing the product image.
- Production four-model test through New API channel 90, unchanged prompt/streaming/model parameters: gpt-5.6-sol HTTP200/completed, TTFT 7.463s; gpt-6.1-sol HTTP200/completed, TTFT 13.437s; gpt-6-astra HTTP200/completed, TTFT 6.276s; gpt-6-sol HTTP200/completed, TTFT 6.520s. The 13.437s request logged attempt_count=2; other three used one attempt, demonstrating live retry recovery. These four successes are a bounded smoke sample, not a reliability SLO.
- Basic provider-edge request from host: HTTP200, TLS0.164s/total0.175s; this checks basic connectivity, not authenticated inference latency. Normal successful TTFT was not materially reduced. The remaining 6–7s wait is within the VM2API kernel/provider inference path and is not resolved by this patch; retries can make an individual request slower while avoiding a transient failure. No lowered reasoning or altered model was used to claim a speedup.
