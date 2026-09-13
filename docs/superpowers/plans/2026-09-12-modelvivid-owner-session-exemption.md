# ModelVivid Owner Session Exemption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow the single configured ModelVivid owner account to create persistent login sessions after the normal active-session or issuance-window limits are reached, without weakening any other authentication control.

**Architecture:** A fail-closed owner predicate in `model/` compares a server-only numeric user ID with the authoritative role and status already loaded by each login path. The ordinary login path and the transactional 2FA/Passkey completion path call the same predicate and skip only the two count checks. Production configuration resolves `mumuup` to a stable database ID before deployment and refuses to continue unless it is the only enabled root account.

**Tech Stack:** Go 1.25.1, GORM v2, Gin authentication services, SQLite/MySQL/PostgreSQL, POSIX shell, Docker Compose.

## Global Constraints

- Start execution from a clean isolated worktree based on the freshly fetched `origin/main`; never pull over the current dirty ModelVivid checkout.
- `MODELVIVID_OWNER_USER_ID` is server-only, numeric, positive, and never accepted from a browser request.
- The owner must match the configured ID, `common.RoleRootUser`, and `common.UserStatusEnabled`; missing or invalid configuration fails closed.
- Only `USER_SESSION_ACTIVE_LIMIT` and `USER_SESSION_ISSUANCE_LIMIT` checks are bypassed for the owner.
- Every successful owner login still creates a normal persistent `user_sessions` row and remains visible/revocable in 登录会话.
- Password, OAuth, 2FA, Passkey, status, role, `auth_version`, flow expiry/single-use, refresh rotation/reuse detection, CSRF/Origin checks, revocation, and audit behavior remain unchanged.
- Other root/admin/common users retain the existing 409/429 limits.
- Authentication changes must be reviewed against OWASP ASVS 5.0 session controls plus the OWASP Authentication and Session Management Cheat Sheets before implementation.
- Do not modify production data until the read-only owner check reports exactly one enabled root named `mumuup` and no conflict.

---

### Task 1: Create an Isolated, Current Upstream Worktree

**Files:**
- No repository file changes.

**Interfaces:**
- Consumes: current checkout at `/Users/liujinmu/IdeaProjects/new-api` and remote `origin/main`.
- Produces: isolated branch `codex/modelvivid-owner-session-exemption` based on the newest fetched `origin/main`.

- [ ] **Step 1: Record and fetch upstream state without altering the dirty checkout**

```bash
cd /Users/liujinmu/IdeaProjects/new-api
git status --short
git fetch --prune origin
git rev-parse origin/main
git log -1 --format='REMOTE=%H%nSUBJECT=%s' origin/main
```

Expected: fetch succeeds; the existing staged/untracked ModelVivid files remain listed exactly as before.

- [ ] **Step 2: Create the isolated worktree using the worktree skill**

```bash
worktree_dir=/Users/liujinmu/IdeaProjects/new-api-owner-session
test ! -e "$worktree_dir"
git worktree add -b codex/modelvivid-owner-session-exemption "$worktree_dir" origin/main
git -C "$worktree_dir" status --short
```

Expected: the new worktree is clean and its `HEAD` equals the fetched `origin/main` revision.

- [ ] **Step 3: Read the applicable security guidance and record the controls in the eventual change summary**

Open and review:

- `https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html`
- `https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html`
- OWASP ASVS 5.0 controls for session creation, renewal, invalidation, and concurrent sessions.

Expected: no implementation starts until the review confirms that the exception remains server-side, identity-bound, logged, and revocable.

### Task 2: Add the Fail-Closed Owner Predicate and Ordinary Login Regression

**Files:**
- Create: `model/modelvivid_owner.go`
- Modify: `service/auth_session.go:63-103`
- Test: `service/auth_session_test.go`

**Interfaces:**
- Consumes: `userID`, `role`, and `status` from an authoritative `model.UserBase` or `model.UserVerificationState`.
- Produces: `model.IsModelVividOwnerIdentity(userID, role, status int) bool`.

- [ ] **Step 1: Add failing ordinary-login cases to the existing session test file**

Add one table-driven test that creates enough rows to reach both limits, then covers these exact cases:

```go
func TestCreateLoginSessionOwnerLimitPolicy(t *testing.T) {
	useTestSessionSecret(t)
	owner := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", owner.Id).Updates(map[string]any{
		"username": "mumuup",
		"role":     common.RoleRootUser,
	}).Error)

	t.Setenv("MODELVIVID_OWNER_USER_ID", strconv.Itoa(owner.Id))
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 1
	seedAtLimit := func(sid string) {
		require.NoError(t, model.DB.Where("user_id = ?", owner.Id).Delete(&model.UserSession{}).Error)
		now := time.Now().Unix()
		require.NoError(t, model.DB.Create(&model.UserSession{
			SID: sid, UserID: owner.Id, Version: 1, UserAuthVersion: owner.AuthVersion,
			Status: model.UserSessionStatusActive, RefreshHash: sid + "-hash",
			LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}
	seedAtLimit("owner-existing")

	bundle, err := CreateLoginSession(owner.Id, "password", "127.0.0.1", "owner-test")
	require.NoError(t, err)
	assert.NotEmpty(t, bundle.Session.SID)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("user_id = ?", owner.Id).Count(&count).Error)
	assert.Equal(t, int64(2), count)

	for _, tc := range []struct {
		name string
		env  string
		role int
	}{
		{name: "missing configuration", env: "", role: common.RoleRootUser},
		{name: "invalid configuration", env: "not-a-user-id", role: common.RoleRootUser},
		{name: "configured user is not root", env: strconv.Itoa(owner.Id), role: common.RoleCommonUser},
		{name: "different root", env: strconv.Itoa(owner.Id + 1), role: common.RoleRootUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MODELVIVID_OWNER_USER_ID", tc.env)
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", owner.Id).Update("role", tc.role).Error)
			seedAtLimit("non-owner-" + strings.ReplaceAll(tc.name, " ", "-"))
			_, err := CreateLoginSession(owner.Id, "password", "127.0.0.1", "non-owner-test")
			assert.ErrorIs(t, err, model.ErrUserSessionLimit)
		})
	}
}
```

Add `strconv` to the existing imports. Keep the fixture local to `service/auth_session_test.go` rather than adding a second test file.

- [ ] **Step 2: Run the focused test and verify RED**

```bash
go test ./service -run TestCreateLoginSessionOwnerLimitPolicy -count=1
```

Expected: FAIL because the configured owner still receives `model.ErrUserSessionLimit`.

- [ ] **Step 3: Add the minimal shared owner predicate**

Create `model/modelvivid_owner.go`:

```go
package model

import (
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const modelVividOwnerUserIDEnv = "MODELVIVID_OWNER_USER_ID"

func IsModelVividOwnerIdentity(userID, role, status int) bool {
	raw := strings.TrimSpace(os.Getenv(modelVividOwnerUserIDEnv))
	configuredID, err := strconv.Atoi(raw)
	return err == nil && configuredID > 0 &&
		userID == configuredID &&
		role == common.RoleRootUser &&
		status == common.UserStatusEnabled
}
```

Do not cache the environment value, accept a username, query by username at request time, or expose the configured ID through an API.

- [ ] **Step 4: Skip only the two count checks in the ordinary login path**

In `createLoginSession`, retain the existing enabled/auth-version checks, then wrap only the active and issuance count block:

```go
	if !model.IsModelVividOwnerIdentity(user.Id, user.Role, user.Status) {
		now := time.Now().Unix()
		activeCount, err := model.CountActiveUserSessions(userID, now)
		if err != nil {
			return nil, err
		}
		if activeCount >= int64(common.UserSessionActiveLimit) {
			return nil, model.ErrUserSessionLimit
		}
		issuanceCount, err := model.CountUserSessionsCreatedSince(userID, now-common.UserSessionIssuanceWindowSeconds)
		if err != nil {
			return nil, err
		}
		if issuanceCount >= int64(common.UserSessionIssuanceLimit) {
			return nil, model.ErrUserSessionIssuanceLimit
		}
	}
```

The owner must continue through `newLoginSession`, `model.CreateUserSession`, and `issueAuthBundle` unchanged.

- [ ] **Step 5: Run focused and existing session tests**

```bash
gofmt -w model/modelvivid_owner.go service/auth_session.go service/auth_session_test.go
go test ./service -run 'TestCreateLoginSessionOwnerLimitPolicy|TestCreateLoginSessionEnforcesActiveLimitAcrossAuthVersions|TestCreateLoginSessionEnforcesIssuanceLimitAcrossAllStatuses|TestCreateLoginSessionFailsClosedWhenLimitCountFails' -count=1
```

Expected: PASS; the owner case creates another persisted session, while every ordinary-user and failure-path assertion remains unchanged.

- [ ] **Step 6: Commit the ordinary path**

```bash
git add model/modelvivid_owner.go service/auth_session.go service/auth_session_test.go
git diff --cached --check
git commit -m "feat(auth): exempt configured owner from session counts"
```

### Task 3: Apply the Same Predicate to 2FA and Passkey Completion

**Files:**
- Modify: `model/login_verification.go:51-88`
- Test: `service/auth_session_test.go`

**Interfaces:**
- Consumes: `model.IsModelVividOwnerIdentity` from Task 2 and the locked `UserVerificationState` loaded inside the transaction.
- Produces: identical session-limit behavior for `model.CreateUserSessionFromLoginFlow`.

- [ ] **Step 1: Add a failing transactional-login test**

Extend `service/auth_session_test.go` with one deterministic test that:

1. Configures the enabled root test user as `MODELVIVID_OWNER_USER_ID`.
2. Sets both limits to one and inserts one qualifying `user_sessions` row.
3. Creates an unconsumed `AuthFlowPurposeLoginVerification` flow with `model.CreateAuthFlow`.
4. Builds a second session with the existing `newLoginSession` helper.
5. Calls `model.CreateUserSessionFromLoginFlow(token, session, func(*model.AuthFlow, *model.UserVerificationState) error { return nil })`.
6. Asserts success, a second persisted session row, and a consumed flow.
7. Repeats with an enabled root whose ID is not configured and asserts `model.ErrUserSessionLimit` plus an unconsumed flow after rollback.

Use the real database transaction and real `AuthFlow`; do not mock the count queries or the predicate.

The core of the test is:

```go
func TestLoginVerificationOwnerLimitPolicy(t *testing.T) {
	useTestSessionSecret(t)
	owner := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", owner.Id).Updates(map[string]any{
		"username": "mumuup",
		"role":     common.RoleRootUser,
	}).Error)
	t.Setenv("MODELVIVID_OWNER_USER_ID", strconv.Itoa(owner.Id))
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 1
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "verification-existing", UserID: owner.Id, Version: 1, UserAuthVersion: owner.AuthVersion,
		Status: model.UserSessionStatusActive, RefreshHash: "verification-existing-hash",
		LoginMethod: "password", CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeLoginVerification,
		UserId: owner.Id,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	session, _, err := newLoginSession(owner.Id, owner.AuthVersion, "password", "127.0.0.1", "owner-2fa-test")
	require.NoError(t, err)
	require.NoError(t, model.CreateUserSessionFromLoginFlow(token, session,
		func(*model.AuthFlow, *model.UserVerificationState) error { return nil }))

	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("user_id = ?", owner.Id).Count(&count).Error)
	assert.Equal(t, int64(2), count)
	_, err = model.GetAuthFlow(token, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeLoginVerification})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
}
```

Add a second subcase in the same test that configures a different owner ID, expects `model.ErrUserSessionLimit`, and verifies `model.GetAuthFlow` still succeeds because the transaction rolled back.

- [ ] **Step 2: Run the focused test and verify RED**

```bash
go test ./service -run TestLoginVerificationOwnerLimitPolicy -count=1
```

Expected: FAIL with `model.ErrUserSessionLimit` for the configured owner.

- [ ] **Step 3: Wrap only the transactional count checks**

After `validate(flow, state)` succeeds in `CreateUserSessionFromLoginFlow`, keep the row lock and transaction, but run the two count queries only when:

```go
		if !IsModelVividOwnerIdentity(state.UserID, state.Role, state.Status) {
			// existing active and issuance count checks remain byte-for-byte equivalent
		}
```

Always call `createUserSessionWithTx(tx, session)` afterwards. Do not move credential validation, flow consumption, user locking, status/auth-version checks, or session creation outside the transaction.

- [ ] **Step 4: Run transactional, flow, 2FA, and Passkey regressions**

```bash
gofmt -w model/login_verification.go service/auth_session_test.go
go test ./service -run 'TestLoginVerificationOwnerLimitPolicy|TestCreateLoginSessionOwnerLimitPolicy' -count=1
go test ./controller -run 'TestSecurityLoginSessionFailureRollsBackChallengeConsumption|Test.*Login.*Passkey|Test.*Login.*TwoFA' -count=1
go test ./model -run 'Test.*AuthFlow|Test.*UserSession' -count=1
```

Expected: PASS, including rollback/single-use behavior for failed factor completion.

- [ ] **Step 5: Commit the transactional path**

```bash
git add model/login_verification.go service/auth_session_test.go
git diff --cached --check
git commit -m "fix(auth): share owner exemption across login flows"
```

### Task 4: Add Production Owner Resolution and Configuration Guards

**Files:**
- Modify: `.env.example`
- Modify: `deploy/production/.env.example`
- Modify: `deploy/production/compose.yaml`
- Create: `deploy/production/scripts/resolve-owner-user.sh`
- Test: `deploy/production/tests/resolve_owner_user_test.sh`

**Interfaces:**
- Consumes: the running MySQL service, username `mumuup`, and the production `.env` file.
- Produces: exactly one `MODELVIVID_OWNER_USER_ID=<positive integer>` line written to `.env` with mode 0600, or a non-zero exit without changing `.env`.

- [ ] **Step 1: Write the failing shell test with a fake MySQL client**

The test must cover:

- one enabled root `mumuup` and exactly one root overall: writes ID;
- zero or two normalized `mumuup` rows: fails and leaves `.env` unchanged;
- `mumuup` disabled or not root: fails unchanged;
- a second root user: fails unchanged;
- non-numeric/non-positive returned ID: fails unchanged;
- output/error streams never contain passwords, SQL DSNs, emails, session tokens, or the full database row.

Use a temporary copied deployment directory, a `PATH`-injected fake `docker` executable, and checksum assertions following `deploy/production/tests/generate_env_test.sh`.

- [ ] **Step 2: Run the shell test and verify RED**

```bash
sh deploy/production/tests/resolve_owner_user_test.sh
```

Expected: FAIL because `scripts/resolve-owner-user.sh` does not exist.

- [ ] **Step 3: Implement the guarded resolver**

`resolve-owner-user.sh` must:

1. Use `set -eu`, fixed owner username `mumuup`, and `umask 077`.
2. Require an existing mode-0600 `.env` and a healthy Compose MySQL service.
3. Query only `id`, `status`, and `role`; count normalized usernames and root rows separately.
4. Require `LOWER(TRIM(username)) = 'mumuup'` count 1, enabled status 1, role 100, and total non-deleted root count 1.
5. Write a new temporary file in the deployment directory, replacing or appending exactly one owner-ID line.
6. `chmod 600`, validate the temporary file, atomically `mv` it over `.env`, and remove it on every failure signal.
7. Print only `OWNER_USER_ID_RESOLVED` on success and a generic conflict category on failure.

Do not put database credentials or the owner ID on the command line, URL, or logs.

- [ ] **Step 4: Wire the environment into the application container**

Add to `deploy/production/.env.example`:

```dotenv
# Stable numeric ID resolved from the single enabled root account named mumuup.
MODELVIVID_OWNER_USER_ID=
```

Add to the `new-api.environment` section of `deploy/production/compose.yaml`:

```yaml
      MODELVIVID_OWNER_USER_ID: "${MODELVIVID_OWNER_USER_ID:?run scripts/resolve-owner-user.sh first}"
```

Document the variable in root `.env.example` without placing a real production ID in source control.

- [ ] **Step 5: Run shell and Compose validation**

```bash
sh deploy/production/tests/generate_env_test.sh
sh deploy/production/tests/resolve_owner_user_test.sh
docker compose --env-file deploy/production/.env.example -f deploy/production/compose.yaml config >/tmp/modelvivid-owner-compose.yaml
grep -q 'MODELVIVID_OWNER_USER_ID' /tmp/modelvivid-owner-compose.yaml
```

Expected: both shell tests pass and Compose renders the required server-only environment variable.

- [ ] **Step 6: Commit deployment guards**

```bash
git add .env.example deploy/production/.env.example deploy/production/compose.yaml deploy/production/scripts/resolve-owner-user.sh deploy/production/tests/resolve_owner_user_test.sh
git diff --cached --check
git commit -m "feat(deploy): resolve the unique ModelVivid owner"
```

### Task 5: Verify Security Invariants and Prepare a Safe Deployment

**Files:**
- Modify only if a focused regression exposes a defect in the files already listed above.

**Interfaces:**
- Consumes: Tasks 1-4 and the production owner-resolution preflight.
- Produces: a verified commit series and deployment commands; no production mutation occurs until the user runs the guarded resolver and reviews the result.

- [ ] **Step 1: Run focused authentication verification**

```bash
go test ./service ./model ./controller -run 'OwnerLimitPolicy|LoginSession|LoginVerification|AuthFlow|Passkey|TwoFA' -count=1
go vet ./service ./model ./controller
```

Expected: PASS with no race-independent authentication regression.

- [ ] **Step 2: Run the complete root-module test suite**

```bash
go test ./... -count=1
```

Expected: PASS. If an unrelated existing failure appears, record its exact command and output; do not claim full verification.

- [ ] **Step 3: Verify all three supported databases**

Run the affected session and owner tests against real supported SQLite, MySQL >= 5.7.8, and PostgreSQL >= 9.6 instances using the repository's configured test DSNs. Run application startup/migration twice for each database.

Expected: the predicate has no schema dependency, session creation remains persistent, and a second startup performs no repeated destructive migration.

- [ ] **Step 4: Re-fetch upstream and check integration before deployment**

```bash
git fetch --prune origin
git rev-list --left-right --count HEAD...origin/main
git log -1 --format='REMOTE=%H%nSUBJECT=%s' origin/main
git status --short
```

Expected: worktree clean. If `origin/main` advanced, rebase the feature branch, resolve conflicts without dropping ModelVivid changes, then rerun Steps 1-3.

- [ ] **Step 5: Run the production read-only/guarded owner resolution**

On the server, back up the mode-0600 environment file, run the resolver, and stop immediately unless it prints only `OWNER_USER_ID_RESOLVED`. Never paste the resulting numeric ID into chat.

- [ ] **Step 6: Deploy and verify observable behavior**

After database backup and a successful image build:

1. Confirm a normal test user at the active limit still receives HTTP 409 and at the issuance limit still receives HTTP 429.
2. Confirm a non-owner root at either limit is rejected.
3. Confirm `mumuup` can complete password and 2FA/Passkey login above both limits.
4. Confirm the new owner session appears in 登录会话 and can be individually revoked.
5. Confirm `退出其他登录会话`, password change, account disable, role downgrade, and refresh-token reuse detection still revoke/reject owner sessions.
6. Confirm no owner ID, refresh token, password, factor code, or session token appears in deployment/application logs.

Expected: only the two owner count checks differ; every other security invariant remains active.

- [ ] **Step 7: Record final evidence and commit any verification-only corrections**

```bash
git status --short
git log --oneline origin/main..HEAD
git diff --check origin/main...HEAD
```

Record exact upstream revision, Go version, database versions, test commands/results, deployed image revision, and OWASP controls reviewed in the final handoff.
