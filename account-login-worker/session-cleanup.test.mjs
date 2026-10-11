import test from 'node:test'
import assert from 'node:assert/strict'

import { cleanupActiveSessions, planSessionCleanup } from './session-cleanup.mjs'

test('session cleanup keeps the single current session and plans every other logout', () => {
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { id: 'phone', current: false, logoutable: true },
    { id: 'laptop', current: false, logoutable: true },
  ]), {
    status: 'ready',
    currentId: 'current',
    logoutIds: ['phone', 'laptop'],
  })
})

test('session cleanup refuses to act when the current session is ambiguous', () => {
  assert.deepEqual(planSessionCleanup([
    { id: 'one', current: true, logoutable: false },
    { id: 'two', current: true, logoutable: false },
    { id: 'other', current: false, logoutable: true },
  ]), {
    status: 'unverified',
    reason: 'current_session_ambiguous',
  })
})

test('session cleanup refuses to leave an unrecognized non-current session active', () => {
  assert.deepEqual(planSessionCleanup([
    { id: 'current', current: true, logoutable: false },
    { id: 'other', current: false, logoutable: false },
  ]), {
    status: 'unverified',
    reason: 'session_not_logoutable',
  })
})

test('session cleanup treats an empty or missing session list as unsupported', () => {
  for (const rows of [[], null, undefined]) {
    assert.deepEqual(planSessionCleanup(rows), {
      status: 'unsupported',
      reason: 'sessions_not_found',
    })
  }
})

test('session cleanup refuses rows without stable unique identities', () => {
  for (const rows of [[
    { id: 'current', current: true, logoutable: false },
    { current: false, logoutable: false },
  ], [
    { id: 'current', current: true, logoutable: false },
    { id: 'current', current: false, logoutable: true },
  ]]) {
    assert.deepEqual(planSessionCleanup(rows), {
      status: 'unverified', reason: 'session_identity_unavailable',
    })
  }
})

test('session cleanup clicks only non-current sessions and reports the count', async () => {
  const clicked = []
  const page = {
    goto: async () => {},
    url: () => 'https://chatgpt.com/#settings/Security',
    evaluate: async callback => {
      const source = callback.toString()
      if (source.includes('document.body?.innerText')) return 'Active sessions'
      if (source.includes('candidates')) return [
        { id: 'current', current: true, logoutable: false },
        ...clicked.length < 1 ? [{ id: 'phone', current: false, logoutable: true }] : [],
        ...clicked.length < 2 ? [{ id: 'laptop', current: false, logoutable: true }] : [],
      ]
      if (source.includes('data-new-api-session-id') && source.includes('button.click')) {
        clicked.push('logout')
        return true
      }
      if (source.includes('sessionId')) return true
      if (source.includes('dialogs')) return true
      throw new Error(`unexpected evaluation: ${source}`)
    },
    waitForTimeout: async () => {},
  }
  assert.deepEqual(await cleanupActiveSessions(page, new AbortController().signal), {
    status: 'success', currentId: 'current', loggedOut: 2,
  })
  assert.deepEqual(clicked, ['logout', 'logout'])
})
