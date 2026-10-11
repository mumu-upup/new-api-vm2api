const sessionSettingsURL = 'https://chatgpt.com/#settings/Security'
const currentSessionPattern = /\bcurrent\s+(?:session|device|browser)\b|当前会话|当前设备|当前浏览器|此设备/i
const logoutButtonPattern = /^(?:log out(?: and remove)?|log out of (?:this )?device|退出登录|退出并移除)$/i
const sessionEvidencePattern = /\bsession\b|\bdevice\b|\bbrowser\b|\b(?:chrome|safari|firefox|edge|windows|macos|android|iphone|ipad)\b|会话|设备|浏览器|当前/i

export const sessionCleanupStatuses = Object.freeze({
  success: 'success',
  ready: 'ready',
  unverified: 'unverified',
  unsupported: 'unsupported',
  failed: 'failed',
})

export function planSessionCleanup(rows) {
  if (!Array.isArray(rows) || rows.length === 0) {
    return { status: sessionCleanupStatuses.unsupported, reason: 'sessions_not_found' }
  }
  const ids = rows.map(row => row?.id)
  if (ids.some(id => typeof id !== 'string' || !id) || new Set(ids).size !== ids.length) {
    return { status: sessionCleanupStatuses.unverified, reason: 'session_identity_unavailable' }
  }
  const current = rows.filter(row => row?.current === true)
  if (current.length !== 1 || !current[0]?.id) {
    return { status: sessionCleanupStatuses.unverified, reason: 'current_session_ambiguous' }
  }
  const others = rows.filter(row => row?.id && row.id !== current[0].id)
  if (others.some(row => row.logoutable !== true)) {
    return { status: sessionCleanupStatuses.unverified, reason: 'session_not_logoutable' }
  }
  return {
    status: sessionCleanupStatuses.ready,
    currentId: current[0].id,
    logoutIds: others.map(row => row.id),
  }
}

function deadlineRemaining(deadline) {
  return Math.max(1, Math.min(5000, deadline - Date.now()))
}

async function waitFor(page, predicate, deadline) {
  while (Date.now() < deadline) {
    if (await predicate()) return true
    await page.waitForTimeout(Math.min(250, deadlineRemaining(deadline)))
  }
  return false
}

export async function collectSessionRows(page) {
  return page.evaluate(({ currentPattern, logoutPattern, sessionEvidence }) => {
    const current = new RegExp(currentPattern, 'i')
    const logout = new RegExp(logoutPattern, 'i')
    const evidence = new RegExp(sessionEvidence, 'i')
    const visible = element => {
      const style = window.getComputedStyle(element)
      return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
    }
    const text = element => (element.innerText || element.textContent || '').replace(/\s+/g, ' ').trim()
    const rowFor = element => element.closest('[role="row"], li, tr, article, [data-testid*="session" i]') || null
    const candidates = new Set()
    for (const element of document.querySelectorAll('button, [role="button"]')) {
      if (!visible(element) || !logout.test(text(element))) continue
      const row = rowFor(element)
      candidates.add(row || { unrecognized: true })
    }
    for (const element of document.querySelectorAll('[role="row"], li, tr, article, [data-testid*="session" i]')) {
      if (!visible(element) || !evidence.test(text(element))) continue
      candidates.add(rowFor(element))
    }
    const rows = []
    let index = 0
    for (const row of candidates) {
      if (row?.unrecognized) {
        rows.push({ id: `new-api-unrecognized-${index++}`, current: false, logoutable: false })
        continue
      }
      if (!row || !visible(row)) continue
      const id = `new-api-session-${index++}`
      row.setAttribute('data-new-api-session-id', id)
      const buttons = [...row.querySelectorAll('button, [role="button"]')]
        .filter(button => visible(button) && logout.test(text(button)))
      rows.push({ id, current: current.test(text(row)), logoutable: buttons.length === 1 && !buttons[0].disabled && evidence.test(text(row)) })
    }
    return rows
  }, {
    currentPattern: currentSessionPattern.source,
    logoutPattern: logoutButtonPattern.source,
    sessionEvidence: sessionEvidencePattern.source,
  })
}

async function clickSessionLogout(page, id) {
  return page.evaluate(({ id, logoutPattern }) => {
    const logout = new RegExp(logoutPattern, 'i')
    const row = document.querySelector(`[data-new-api-session-id="${CSS.escape(id)}"]`)
    if (!row) return false
    const visible = element => {
      const style = window.getComputedStyle(element)
      return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
    }
    const button = [...row.querySelectorAll('button, [role="button"]')]
      .find(candidate => visible(candidate) && logout.test((candidate.innerText || candidate.textContent || '').replace(/\s+/g, ' ').trim()) && !candidate.disabled)
    if (!button) return false
    button.click()
    return true
  }, { id, logoutPattern: logoutButtonPattern.source })
}

async function clickConfirmation(page, deadline) {
  return waitFor(page, async () => page.evaluate(({ logoutPattern }) => {
    const logout = new RegExp(logoutPattern, 'i')
    const visible = element => {
      const style = window.getComputedStyle(element)
      return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
    }
    const dialogs = [...document.querySelectorAll('[role="dialog"], dialog')].filter(visible)
    if (dialogs.length !== 1) return false
    const button = [...dialogs[0].querySelectorAll('button, [role="button"]')]
      .find(candidate => visible(candidate) && logout.test((candidate.innerText || candidate.textContent || '').replace(/\s+/g, ' ').trim()) && !candidate.disabled)
    if (!button) return false
    button.click()
    return true
  }, { logoutPattern: logoutButtonPattern.source }), deadline)
}

async function waitForSessionRowGone(page, id, deadline) {
  return waitFor(page, async () => page.evaluate(sessionId => {
    const row = document.querySelector(`[data-new-api-session-id="${CSS.escape(sessionId)}"]`)
    if (!row) return true
    const style = window.getComputedStyle(row)
    return style.display === 'none' || style.visibility === 'hidden' || row.getClientRects().length === 0
  }, id), deadline)
}

export async function cleanupActiveSessions(page, signal, options = {}) {
  const deadline = options.deadline || Date.now() + 60000
  if (signal.aborted) return { status: sessionCleanupStatuses.failed, reason: 'login_timeout' }
  try {
    await page.goto(sessionSettingsURL, { waitUntil: 'domcontentloaded', timeout: deadlineRemaining(deadline) })
    const loaded = await waitFor(page, async () => {
      const url = page.url()
      if (!url.startsWith('https://chatgpt.com/')) return false
      const text = await page.evaluate(() => (document.body?.innerText || '').replace(/\s+/g, ' '))
      return /active sessions|security(?: and login)?|当前会话|活动会话/i.test(text)
    }, deadline)
    if (!loaded) return { status: sessionCleanupStatuses.unverified, reason: 'security_settings_unavailable' }
    let plan = planSessionCleanup(await collectSessionRows(page))
    if (plan.status !== sessionCleanupStatuses.ready) return plan
    let loggedOut = 0
    while (plan.logoutIds.length > 0) {
      if (signal.aborted) return { status: sessionCleanupStatuses.failed, reason: 'login_timeout' }
      if (Date.now() >= deadline) return { status: sessionCleanupStatuses.failed, reason: 'login_timeout' }
      const id = plan.logoutIds[0]
      if (!await clickSessionLogout(page, id)) {
        return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
      }
      if (!await clickConfirmation(page, deadline)) {
        return { status: sessionCleanupStatuses.unverified, reason: 'logout_confirmation_unavailable' }
      }
      if (!await waitForSessionRowGone(page, id, deadline)) {
        return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
      }
      loggedOut++
      await page.waitForTimeout(Math.min(500, deadlineRemaining(deadline)))
      plan = planSessionCleanup(await collectSessionRows(page))
      if (plan.status !== sessionCleanupStatuses.ready) return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
    }
    return { status: sessionCleanupStatuses.success, currentId: plan.currentId, loggedOut }
  } catch (error) {
    if (signal.aborted) return { status: sessionCleanupStatuses.failed, reason: 'login_timeout' }
    options.onDiagnostic?.({ event: 'session_cleanup_failed', reason: error?.name === 'TimeoutError' ? 'timeout' : 'browser_error' })
    return { status: sessionCleanupStatuses.failed, reason: 'browser_error' }
  }
}

export { sessionSettingsURL }
