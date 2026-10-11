(function exposeSessionCleanup(root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  root.NewApiSessionCleanup = api
})(globalThis, () => {
  const currentSessionPattern = /\bcurrent\s+(?:session|device|browser)\b|当前会话|当前设备|当前浏览器|此设备/i
  const logoutButtonPattern = /^(?:log out(?: and remove)?|log out of (?:this )?device|退出登录|退出并移除)$/i
  const sessionEvidencePattern = /\bsession\b|\bdevice\b|\bbrowser\b|\b(?:chrome|safari|firefox|edge|windows|macos|android|iphone|ipad)\b|会话|设备|浏览器|当前/i
  const sessionCleanupStatuses = Object.freeze({
    success: 'success',
    ready: 'ready',
    unverified: 'unverified',
    unsupported: 'unsupported',
    failed: 'failed',
  })

  const normalizeText = element => (element?.innerText || element?.textContent || '')
    .replace(/\s+/g, ' ').trim()

  function planSessionCleanup(rows) {
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

  function isVisible(element) {
    if (!element || element.hidden || element.disabled) return false
    const style = globalThis.getComputedStyle?.(element)
    return style?.display !== 'none' && style?.visibility !== 'hidden' && element.getClientRects?.().length > 0
  }

  function rowFor(element) {
    return element.closest?.('[role="row"], li, tr, article, [data-testid*="session" i]') || null
  }

  function collectSessionRows(document) {
    if (!document) return []
    const current = new RegExp(currentSessionPattern.source, 'i')
    const logout = new RegExp(logoutButtonPattern.source, 'i')
    const evidence = new RegExp(sessionEvidencePattern.source, 'i')
    const candidates = new Set()
    for (const element of document.querySelectorAll('button, [role="button"]')) {
      if (!isVisible(element) || !logout.test(normalizeText(element))) continue
      const row = rowFor(element)
      candidates.add(row || { unrecognized: true })
    }
    for (const element of document.querySelectorAll('[role="row"], li, tr, article, [data-testid*="session" i]')) {
      if (isVisible(element) && evidence.test(normalizeText(element))) candidates.add(rowFor(element))
    }
    const rows = []
    let index = 0
    for (const row of candidates) {
      if (row?.unrecognized) {
        rows.push({ id: `new-api-unrecognized-${index++}`, current: false, logoutable: false })
        continue
      }
      if (!row || !isVisible(row)) continue
      const id = `new-api-session-${index++}`
      row.setAttribute('data-new-api-session-id', id)
      const buttons = [...row.querySelectorAll('button, [role="button"]')]
        .filter(button => isVisible(button) && logout.test(normalizeText(button)))
      rows.push({ id, current: current.test(normalizeText(row)), logoutable: buttons.length === 1 && evidence.test(normalizeText(row)) })
    }
    return rows
  }

  function clickSessionLogout(document, id) {
    const row = document.querySelector(`[data-new-api-session-id="${CSS.escape(id)}"]`)
    if (!row) return false
    const logout = new RegExp(logoutButtonPattern.source, 'i')
    const button = [...row.querySelectorAll('button, [role="button"]')]
      .find(candidate => isVisible(candidate) && logout.test(normalizeText(candidate)))
    if (!button) return false
    button.click()
    return true
  }

  function clickConfirmation(document) {
    const logout = new RegExp(logoutButtonPattern.source, 'i')
    const dialogs = [...document.querySelectorAll('[role="dialog"], dialog')].filter(isVisible)
    if (dialogs.length !== 1) return false
    const button = [...dialogs[0].querySelectorAll('button, [role="button"]')]
      .find(candidate => isVisible(candidate) && logout.test(normalizeText(candidate)))
    if (!button) return false
    button.click()
    return true
  }

  function sessionRowGone(document, id) {
    const row = document.querySelector(`[data-new-api-session-id="${CSS.escape(id)}"]`)
    if (!row) return true
    const style = globalThis.getComputedStyle?.(row)
    return style?.display === 'none' || style?.visibility === 'hidden' || row.getClientRects?.().length === 0
  }

  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds))

  async function waitFor(predicate, deadline) {
    while (Date.now() < deadline) {
      if (predicate()) return true
      await sleep(Math.min(250, Math.max(1, deadline - Date.now())))
    }
    return false
  }

  async function cleanupActiveSessions(document, options = {}) {
    const deadline = options.deadline || Date.now() + 60000
    try {
      const loaded = await waitFor(() => {
        const text = normalizeText(document.body)
        return /active sessions|security(?: and login)?|当前会话|活动会话/i.test(text)
      }, deadline)
      if (!loaded) return { status: sessionCleanupStatuses.unverified, reason: 'security_settings_unavailable' }
      let plan = planSessionCleanup(collectSessionRows(document))
      if (plan.status !== sessionCleanupStatuses.ready) return plan
      let loggedOut = 0
      while (plan.logoutIds.length > 0) {
        if (options.signal?.aborted) return { status: sessionCleanupStatuses.failed, reason: 'aborted' }
        if (Date.now() >= deadline) return { status: sessionCleanupStatuses.failed, reason: 'timeout' }
        const id = plan.logoutIds[0]
        if (!clickSessionLogout(document, id)) {
          return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
        }
        const confirmed = await waitFor(() => clickConfirmation(document), deadline)
        if (!confirmed) return { status: sessionCleanupStatuses.unverified, reason: 'logout_confirmation_unavailable' }
        if (!await waitFor(() => sessionRowGone(document, id), deadline)) {
          return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
        }
        loggedOut++
        await sleep(Math.min(500, Math.max(1, deadline - Date.now())))
        plan = planSessionCleanup(collectSessionRows(document))
        if (plan.status !== sessionCleanupStatuses.ready) return { status: sessionCleanupStatuses.unverified, reason: 'session_changed' }
      }
      return { status: sessionCleanupStatuses.success, loggedOut }
    } catch {
      return { status: sessionCleanupStatuses.failed, reason: 'browser_error' }
    }
  }

  return { cleanupActiveSessions, collectSessionRows, planSessionCleanup, sessionCleanupStatuses }
})
