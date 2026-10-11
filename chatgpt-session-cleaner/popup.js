const enabled = document.querySelector('#enabled')
const status = document.querySelector('#status')

function describeResult(result) {
  if (!result) return '尚未执行清理。'
  if (result.status === 'success') return `最近一次成功退出 ${result.loggedOut || 0} 个其他会话。`
  if (result.status === 'unverified') return '未能安全确认会话列表，未执行不确定的退出操作。'
  return '最近一次清理未完成，请检查 ChatGPT 安全设置。'
}

chrome.storage.local.get({ armed: false, lastResult: null }).then(settings => {
  enabled.checked = settings.armed
  status.textContent = describeResult(settings.lastResult)
})

enabled.addEventListener('change', () => {
  chrome.storage.local.set({ armed: enabled.checked })
})
