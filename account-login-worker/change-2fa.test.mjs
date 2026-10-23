import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createLoginServer } from './server.mjs'
import { changeTwoFactor, safeChangeResult, validateChangeRequest } from './change-2fa.mjs'

const oldSecret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
const newSecret = 'JBSWY3DPEHPK3PXP'
const input = { email: 'account@example.com', password: 'private password', old_totp_secret: oldSecret }
const headers = { 'content-type': 'application/json', authorization: 'Bearer a-test-token-with-32-characters-minimum' }

async function endpoint(t, change2FA) {
  const server = createLoginServer({ token: 'a-test-token-with-32-characters-minimum', login: async () => assert.fail('login must not run'), change2FA })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => new Promise(resolve => server.close(resolve)))
  return `http://127.0.0.1:${server.address().port}/change-2fa`
}

test('validates bounded change credentials and never accepts malformed secrets', () => {
  assert.deepEqual(validateChangeRequest(input), input)
  for (const bad of [
    { ...input, email: 'not-an-email' },
    { ...input, password: '' },
    { ...input, old_totp_secret: 'not-a-seed' },
    { ...input, email: 'a'.repeat(321) },
  ]) assert.throws(() => validateChangeRequest(bad), /invalid_request/)
})

test('accepts a verified primary replacement and returns only the replacement on success', async () => {
  const result = await changeTwoFactor(input, new AbortController().signal, {
    primary: async () => ({ status: 'success', new_totp_secret: `otpauth://totp/OpenAI?secret=${newSecret}`, verified: true }),
    fallback: async () => assert.fail('fallback must not run'),
  })
  assert.deepEqual(result, { status: 'success', stage: 'primary', fallback_used: false, new_totp_secret: newSecret })
})

test('uses Moyanovo once after a classified primary failure', async () => {
  let fallbackCalls = 0
  const result = await changeTwoFactor(input, new AbortController().signal, {
    primary: async () => ({ status: 'failed', error_code: 'security_flow_unavailable', recoverable: true }),
    fallback: async ({ forceEmailSkip }) => {
      fallbackCalls++
      assert.equal(forceEmailSkip, false)
      return { status: 'success', new_totp_secret: newSecret, verified: true }
    },
  })
  assert.equal(fallbackCalls, 1)
  assert.deepEqual(result, { status: 'success', stage: 'fallback', fallback_used: true, new_totp_secret: newSecret })
})

test('rejects an unverified or malformed fallback replacement and preserves a sanitized error', async () => {
  const result = await changeTwoFactor(input, new AbortController().signal, {
    primary: async () => ({ status: 'failed', error_code: 'security_flow_unavailable', recoverable: true }),
    fallback: async () => ({ status: 'success', new_totp_secret: 'password=private', verified: false }),
  })
  assert.deepEqual(result, { status: 'failed', stage: 'fallback', fallback_used: true, error_code: 'replacement_unverified' })
  assert.doesNotMatch(JSON.stringify(safeChangeResult({ ...result, error: 'password=private GEZDGNBVGY3TQOJQ' })), /private|GEZDG/)
})

test('maps cancellation and timeout without leaking credentials', async () => {
  const controller = new AbortController()
  controller.abort()
  const result = await changeTwoFactor(input, controller.signal, {
    primary: async () => assert.fail('cancelled operation must not call primary'),
    fallback: async () => assert.fail('cancelled operation must not call fallback'),
  })
  assert.deepEqual(result, { status: 'failed', stage: 'primary', fallback_used: false, error_code: 'change_timeout' })
})

test('safe result strips arbitrary adapter errors and secrets from failures', () => {
  assert.deepEqual(safeChangeResult({ status: 'failed', stage: 'primary', fallback_used: false, error_code: 'bad', error: 'password=secret' }),
    { status: 'failed', stage: 'primary', fallback_used: false, error_code: 'automation_unavailable' })
})

test('change endpoint authenticates, validates and sanitizes the worker contract', async t => {
  const url = await endpoint(t, async () => ({ status: 'failed', stage: 'primary', fallback_used: false, error_code: 'security_flow_unavailable', error: 'password=private' }))
  const unauthorized = await fetch(url, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(input) })
  assert.equal(unauthorized.status, 401)
  const response = await fetch(url, { method: 'POST', headers, body: JSON.stringify(input) })
  assert.deepEqual(await response.json(), { status: 'failed', stage: 'primary', fallback_used: false, error_code: 'security_flow_unavailable' })
})
