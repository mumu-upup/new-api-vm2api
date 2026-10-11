(() => {
  const callbackOrigin = 'http://localhost:1455'
  const chatGPTOrigin = 'https://chatgpt.com'
  const settingsHash = '#settings/Security'

  if (location.origin === callbackOrigin && location.pathname === '/auth/callback') {
    const params = new URLSearchParams(location.search)
    if (params.get('code') && params.get('state')) {
      chrome.runtime.sendMessage({ type: 'oauth-callback' })
    }
    return
  }

  if (location.origin !== chatGPTOrigin) return
  if (location.hash.toLowerCase() !== settingsHash.toLowerCase()) return

  chrome.runtime.sendMessage({ type: 'security-settings-ready' }, response => {
    if (chrome.runtime.lastError || !response?.runCleanup) return
    const { cleanupActiveSessions } = globalThis.NewApiSessionCleanup
    cleanupActiveSessions(document, { deadline: Date.now() + 60000 })
      .then(result => chrome.runtime.sendMessage({ type: 'cleanup-result', result }))
      .catch(() => chrome.runtime.sendMessage({
        type: 'cleanup-result',
        result: { status: 'failed', reason: 'browser_error' },
      }))
  })
})()
