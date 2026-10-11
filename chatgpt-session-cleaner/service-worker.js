const settingsURL = 'https://chatgpt.com/#settings/Security'
let callbackInFlight = false

async function readSettings() {
  return chrome.storage.local.get({ armed: false, lastResult: null, pendingTabId: null })
}

async function saveResult(result) {
  const safeResult = {
    status: typeof result?.status === 'string' ? result.status : 'failed',
    ...(Number.isInteger(result?.loggedOut) && result.loggedOut >= 0 && result.loggedOut <= 100
      ? { loggedOut: result.loggedOut }
      : {}),
    ...(typeof result?.reason === 'string' ? { reason: result.reason } : {}),
    at: new Date().toISOString(),
  }
  await chrome.storage.local.set({ lastResult: safeResult })
  await chrome.action.setBadgeText({ text: safeResult.status === 'success' ? String(safeResult.loggedOut || 0) : '!' })
  await chrome.action.setBadgeBackgroundColor({ color: safeResult.status === 'success' ? '#15803d' : '#b91c1c' })
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  const tabId = sender.tab?.id
  if (!Number.isInteger(tabId)) return

  if (message?.type === 'oauth-callback') {
    if (callbackInFlight) return
    callbackInFlight = true
    readSettings().then(({ armed, pendingTabId }) => {
      if (!armed || Number.isInteger(pendingTabId)) return
      return chrome.storage.local.set({ armed: false }).then(() => chrome.tabs.create({ url: 'about:blank' })).then(settingsTab => {
        if (!Number.isInteger(settingsTab.id)) return
        return chrome.storage.local.set({ pendingTabId: settingsTab.id }).then(() => {
          return chrome.tabs.update(settingsTab.id, { url: settingsURL })
        })
      }).then(() => {
        chrome.action.setBadgeText({ text: '…' })
      })
    }).catch(() => {}).finally(() => { callbackInFlight = false })
    return
  }

  if (message?.type === 'security-settings-ready') {
    readSettings().then(({ pendingTabId }) => sendResponse({ runCleanup: pendingTabId === tabId }))
      .catch(() => sendResponse({ runCleanup: false }))
    return true
  }

  if (message?.type === 'cleanup-result') {
    readSettings().then(({ pendingTabId }) => {
      if (pendingTabId !== tabId) return false
      return chrome.storage.local.remove('pendingTabId').then(() => true)
    }).then(shouldSave => {
      if (shouldSave) return saveResult(message.result)
      return undefined
    }).catch(() => {})
  }
})

chrome.tabs.onRemoved.addListener(tabId => {
  readSettings().then(({ pendingTabId }) => {
    if (pendingTabId === tabId) return chrome.storage.local.remove('pendingTabId')
    return undefined
  }).catch(() => {})
})
