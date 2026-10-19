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
