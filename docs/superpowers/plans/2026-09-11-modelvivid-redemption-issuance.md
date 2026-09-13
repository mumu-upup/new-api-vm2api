# ModelVivid Redemption Issuance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a server-to-server, HMAC-authenticated redemption-code issuance service with fixed products, idempotent orders, query/revoke operations, and compatibility with the existing wallet redemption flow.

**Architecture:** Add focused model/service/controller code behind `/api/partner/v1/*` POST routes. Partner authentication reads a server-side partner record, verifies a canonical HMAC request, rejects stale/replayed requests and disallowed IPs, and applies a Redis-backed fixed-window limit. Issuance and redemption state changes stay transactional in MySQL/PostgreSQL/SQLite through GORM; existing administrator redemption behavior remains compatible.

**Tech Stack:** Go, Gin, GORM, Redis, HMAC-SHA256, AES-GCM, Go `crypto/rand`, existing New API quota and logging services.

## Global Constraints

- Partner parameters are JSON request bodies; no order id or code is put in a URL.
- Product codes are limited to `CNY_10`, `CNY_50`, `CNY_100`, `CNY_200`, and `CNY_500`; callers cannot submit arbitrary amount or quota.
- Requests require `X-Partner-Id`, `X-Timestamp`, `X-Nonce`, and `X-Signature`; the signature covers method, path, timestamp, nonce, and SHA-256 body digest.
- Timestamp skew is at most 5 minutes; a nonce is single-use for at least 10 minutes; secrets and plaintext codes never enter ordinary logs or frontend assets.
- The first chain-store integration exports issued codes for manual 链动小铺 卡密库存 import; no undocumented external API is called.
- Preserve existing uncommitted ratio-setting files and legacy administrator redemption codes.

---

### Task 1: Add failing security and product tests

**Files:**
- Create: `service/redemption_partner_test.go`
- Create: `model/redemption_issuance_test.go`
- Create: `controller/redemption_partner_test.go`

**Interfaces:**
- Tests consume the public helpers and handlers introduced in later tasks: `service.BuildPartnerSignature`, `service.VerifyPartnerRequest`, `model.IssuePartnerRedemption`, `model.QueryPartnerOrder`, `model.RevokePartnerOrder`, and `controller.PartnerIssueRedemption`.

- [ ] **Step 1: Write the failing test**

Add tests that assert: canonical HMAC changes when the body changes; stale timestamps and replayed nonces are rejected; disabled/unknown partners and non-allowlisted IPs are rejected; arbitrary product codes are rejected; same partner/order is idempotent; a different product for that order conflicts; and 100 concurrent issuance attempts leave one order and one redemption row.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./service ./model ./controller -run 'Partner|Issuance' -count=1`

Expected: FAIL because the partner service, models, and controller do not yet exist.

- [ ] **Step 3: Keep the test fixtures isolated**

Use SQLite in-memory GORM databases assigned to `model.DB`, disable Redis only for model tests, and restore package globals with `t.Cleanup`; use a fake nonce store in service tests so security tests do not require production Redis.

### Task 2: Implement partner authentication and rate limiting

**Files:**
- Create: `service/redemption_partner.go`
- Modify: `common/redis.go`
- Create: `service/redemption_partner_test.go` (extend tests from Task 1)

**Interfaces:**
- Produces `BuildPartnerSignature(method, path string, timestamp int64, nonce string, body []byte, secret []byte) string`.
- Produces `VerifyPartnerRequest(ctx context.Context, req PartnerRequest) (*model.RedemptionPartner, error)`.
- Produces `PartnerRequest` with partner id, timestamp, nonce, signature, method, path, body, and client IP.

- [ ] **Step 1: Implement canonical signing and verification**

Hash the exact request body with SHA-256, build `METHOD + "\\n" + PATH + "\\n" + timestamp + "\\n" + nonce + "\\n" + bodyHash`, calculate HMAC-SHA256, and compare with `hmac.Equal`. Parse bounded numeric timestamp, enforce 300-second skew, validate nonce length/charset, load an active partner, enforce its CIDR/IP allowlist, and return stable errors without exposing the secret.

- [ ] **Step 2: Add atomic nonce storage and fixed-window limits**

Add a `RedisSetNX` helper using `SETNX` plus expiry. Store `partner:<id>:nonce:<nonce>` for 10 minutes. Use a Redis counter key per partner and minute window; when Redis is disabled, use a bounded process-local map with mutex and expiry so tests and single-node development remain protected.

- [ ] **Step 3: Run the focused security tests**

Run: `go test ./service -run 'Test(BuildPartnerSignature|VerifyPartnerRequest|PartnerNonce|PartnerRateLimit)' -count=1`

Expected: PASS, including body-tamper, stale timestamp, replay, invalid partner, IP rejection, and rate-limit cases.

### Task 3: Add issuance models, encryption, and migrations

**Files:**
- Create: `model/redemption_partner.go`
- Create: `model/redemption_product.go`
- Create: `model/redemption_issuance.go`
- Modify: `model/redemption.go`
- Modify: `model/main.go`
- Create: `model/redemption_issuance_test.go` (extend tests from Task 1)

**Interfaces:**
- Produces `model.IssuePartnerRedemption(partnerID, merchantOrderID, productCode string) (*RedemptionIssuanceOrder, string, error)`.
- Produces `model.QueryPartnerOrder(partnerID, merchantOrderID string) (*RedemptionIssuanceOrder, string, error)`.
- Produces `model.RevokePartnerOrder(partnerID, merchantOrderID, reason string) error`.
- Extends `Redemption` with nullable hash/ciphertext/source/issuance-order fields while keeping legacy `Key` codes valid.

- [ ] **Step 1: Define tables and fixed product seed**

Define `RedemptionPartner`, `RedemptionProduct`, and `RedemptionIssuanceOrder` with unique `(partner_id, merchant_order_id)` and unique `product_code`. Seed the five fixed products only when absent; never overwrite an existing product snapshot. Include status and terminal timestamps.

- [ ] **Step 2: Implement code generation and storage**

Generate 32 random bytes with `crypto/rand`, encode with unpadded base64url, store SHA-256 hash and AES-256-GCM ciphertext using `REDEMPTION_ENCRYPTION_KEY` (base64 or 32-byte value), and return plaintext only from the first issue response or an issued-order query. Refuse issuance when the encryption key is missing rather than storing plaintext.

- [ ] **Step 3: Implement transactional idempotency and revoke**

Resolve a product from the whitelist, create the redemption and issuance order in one transaction, recover an existing issued code by decrypting ciphertext, return a conflict on product mismatch, and lock the order row for revoke. Use stable sentinel errors for not-found, mismatch, redeemed, expired, and revoked states.

- [ ] **Step 4: Register migrations and seed data**

Include the three models in both `migrateDB` and `migrateDBFast`; add new redemption columns through GORM migration. Add a migration test that runs twice and confirms existing legacy rows remain readable.

- [ ] **Step 5: Run model tests**

Run: `go test ./model -run 'Test(RedemptionIssuance|RedemptionCode|RedemptionMigration)' -count=1`

Expected: PASS for idempotency, fixed products, entropy/format, encrypted recovery, terminal-code redaction, and legacy compatibility.

### Task 4: Link redemption consumption to issuance orders

**Files:**
- Modify: `model/redemption.go`
- Create: `model/redemption_redeem_issuance_test.go`

**Interfaces:**
- Existing `Redeem(key, userID)` remains the public entry point and now updates a linked issuance order atomically when the redemption has an issuance order id.

- [ ] **Step 1: Write the race test**

Create one issued partner code and one user, call `Redeem` concurrently 20 times, and assert exactly one success, one quota credit, one `redeemed` issuance order, and no plaintext code in the audit/log output.

- [ ] **Step 2: Update the redemption transaction**

Look up new codes by hash first and legacy codes by `Key`, lock the redemption row, conditionally flip enabled to used, credit the user with the existing quota-ceiling helper, and conditionally transition the linked issuance order from `issued` to `redeemed`. Reject revoked/expired issuance rows before crediting.

- [ ] **Step 3: Run the race test and existing redemption suite**

Run: `go test ./model -run 'Test(Redeem|Redemption)' -count=1`

Expected: PASS for legacy behavior and one-time partner redemption under concurrency.

### Task 5: Expose signed POST partner endpoints

**Files:**
- Create: `controller/redemption_partner.go`
- Modify: `router/api-router.go`
- Create: `controller/redemption_partner_test.go` (extend tests from Task 1)

**Interfaces:**
- `POST /api/partner/v1/redemption-codes` -> `PartnerIssueRedemption`.
- `POST /api/partner/v1/redemption-orders/query` -> `PartnerQueryRedemption`.
- `POST /api/partner/v1/redemption-codes/revoke` -> `PartnerRevokeRedemption`.

- [ ] **Step 1: Add request middleware and body limits**

Read and restore a bounded JSON body, pass method/path/body/IP to `VerifyPartnerRequest`, and apply partner/global rate limits before decoding. All errors use safe stable codes and HTTP 4xx/409 status with no SQL details.

- [ ] **Step 2: Add issue/query/revoke handlers**

Validate non-empty bounded `merchant_order_id`, fixed `product_code`, and bounded revoke reason. Return `order_id`, product snapshot, amount, status, expiry, and the code only while status is `issued`; never return complete codes for terminal orders.

- [ ] **Step 3: Register routes and audit events**

Register the three POST routes under `/api/partner/v1`, attach the authentication handler, and log only partner id, order id hash/reference, product code, status, and client IP. Never log request body, signature, secret, or full code.

- [ ] **Step 4: Run controller and route tests**

Run: `go test ./controller ./router -run 'Test(Partner|Redemption)' -count=1`

Expected: PASS for valid signed requests, tampering, JSON-only parameters, status-safe responses, and route registration.

### Task 6: Add deployment/configuration documentation and verification

**Files:**
- Create: `docs/operations/modelvivid-redemption-partner.md`
- Modify: `.env.example` or the repository's documented environment template
- Create: `deploy/modelvivid/partner-redemption-check.sh`

**Interfaces:**
- Documents the environment variables `REDEMPTION_ENCRYPTION_KEY`, partner provisioning SQL/API procedure, product catalog, signature example, and manual 链动小铺 import workflow.

- [ ] **Step 1: Document secret provisioning and rotation**

Document generating a 32-byte key with `openssl rand -base64 32`, storing it only in the server secret store, setting partner allowlisted IPs, and rotating with overlap. Include a redacted curl/signature example that keeps all business values in JSON.

- [ ] **Step 2: Add a non-destructive health/check script**

Create a shell script that checks required environment variables, database tables, Redis reachability, and endpoint status without issuing a real code or changing chain-store inventory.

- [ ] **Step 3: Run the complete verification**

Run: `gofmt -w service/redemption_partner.go model/redemption_partner.go model/redemption_product.go model/redemption_issuance.go model/redemption.go controller/redemption_partner.go && go test ./service ./model ./controller ./router -count=1 && go vet ./... && git diff --check`

Expected: all focused and affected Go tests pass, vet has no errors, and the diff is clean. Only after this evidence should the built image be deployed and the manual 链动小铺 low-value test performed.

---

## Self-Review

- Spec coverage: Tasks 2-5 cover authentication, replay protection, limits, product whitelist, issuance/query/revoke, encryption, idempotency, redemption races, migrations, and safe responses; Task 6 covers deployment and manual chain-store operations.
- Scope: No undocumented chain-store API, browser secret, arbitrary amount input, or automatic external product mutation is included.
- Type consistency: Controller handlers consume the service request and model methods named above; migration registration uses the three model types defined in Task 3.
- Placeholder scan: This plan contains no `TODO`, `TBD`, or “implement later” steps; all deferred external actions are explicitly out of scope pending official API documentation.
