import test from 'node:test'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const { planSessionCleanup } = require('./cleanup-core.js')

test('the extension keeps the current session and returns every other logout target', () => {
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { id: 'phone', current: false, logoutable: true },
  ]), {
    status: 'ready',
    currentId: 'current',
    logoutIds: ['phone'],
  })
})

test('the extension refuses ambiguous, incomplete, or unrelated logout targets', () => {
  assert.deepEqual(planSessionCleanup([
    { id: 'one', current: true, logoutable: false },
    { id: 'two', current: true, logoutable: false },
  ]), { status: 'unverified', reason: 'current_session_ambiguous' })
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { id: 'other', current: false, logoutable: false },
  ]), { status: 'unverified', reason: 'session_not_logoutable' })
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { id: 'global-logout', current: false, logoutable: false },
  ]), { status: 'unverified', reason: 'session_not_logoutable' })
  assert.deepEqual(planSessionCleanup([]), { status: 'unsupported', reason: 'sessions_not_found' })
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { current: false, logoutable: false },
  ]), { status: 'unverified', reason: 'session_identity_unavailable' })
})
