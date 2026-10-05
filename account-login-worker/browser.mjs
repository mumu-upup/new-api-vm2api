import { setTimeout as delay } from 'node:timers/promises'
import { generateTOTP } from './login.mjs'

const callbackOrigin = 'http://localhost:1455'
const callbackPath = '/auth/callback'
const loginOrigin = 'https://auth.openai.com'

export function parseCallback(raw, state) {
  const url = new URL(raw)
  if (url.origin !== callbackOrigin || url.pathname !== callbackPath) return null
  if (url.username || url.password || url.hash || url.searchParams.getAll('state').length !== 1 ||
      url.searchParams.get('state') !== state || url.searchParams.getAll('code').length !== 1 ||
      !url.searchParams.get('code') || url.searchParams.has('error')) {
    return { status: 'failed', error_code: 'invalid_callback' }
  }
  return { status: 'success', code: url.searchParams.get('code'), state }
}

function permittedRequest(url, method, mainNavigation) {
  if (url.username || url.password || url.protocol !== 'https:') return false
  if (mainNavigation || !['GET', 'HEAD', 'OPTIONS'].includes(method)) return url.origin === loginOrigin
  return ['auth.openai.com', 'openai.com', 'oaistatic.com', 'cdn.auth0.com', 'challenges.cloudflare.com']
    .some(host => url.hostname === host || (host === 'oaistatic.com' && url.hostname.endsWith('.oaistatic.com')))
}

export async function handleLoginRequest(route, guard) {
  const request = route.request()
  let url = new URL(request.url())
  const method = request.method()
  const mainNavigation = request.isNavigationRequest() && request.frame() === guard.mainFrame
  for (let hop = 0; hop < 6; hop++) {
    const callback = parseCallback(url.href, guard.state)
    if (callback && mainNavigation && method === 'GET') {
      guard.setCallback(callback)
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Authorization received.' })
      return
    }
    if (!permittedRequest(url, method, mainNavigation)) break
    // Playwright does not route Chromium's redirect hops. Fetch exactly one hop
    // and inspect its destination before any follow-up can carry credentials.
    const response = await route.fetch({ url: url.href, maxRedirects: 0, timeout: Math.max(1, Math.min(20000, guard.deadline - Date.now())) })
    const status = response.status()
    if (![301, 302, 303, 307, 308].includes(status)) {
      await route.fulfill({ response })
      return
    }
    if (++guard.redirects > 12) break
    const location = response.headers().location
    if (!location) break
    const next = new URL(location, url)
    const nextMethod = status === 303 || ((status === 301 || status === 302) && method === 'POST') ? 'GET' : method
    const nextCallback = parseCallback(next.href, guard.state)
    if (nextCallback && mainNavigation && nextMethod === 'GET') {
      guard.setCallback(nextCallback)
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Authorization received.' })
      return
    }
    if (!permittedRequest(next, nextMethod, mainNavigation) || !['GET', 'HEAD'].includes(nextMethod)) break
    if (mainNavigation) {
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Continuing authorization.' })
      await guard.navigate(next.href, url.href)
      return
    }
    // Preserve subresource semantics only for idempotent GET/HEAD requests.
    if (!['GET', 'HEAD'].includes(method) || next.origin !== url.origin) break
    url = next
  }
  guard.setBlocked()
  await route.abort()
}

export async function completeLoginPage(page, input, signal, options = {}) {
  const now = options.now || Date.now
  const wait = options.wait || (() => delay(300, undefined, { signal }))
  const deadline = options.deadline || now() + 150000
  const submitted = new Set()
  let previousKind = ''
  for (let step = 0; step < 500 && now() < deadline; step++) {
    if (signal.aborted) break
    const callback = parseCallback(page.url(), input.state)
    if (callback) return callback
    if (new URL(page.url()).origin !== loginOrigin) {
      return { status: 'needs_attention', error_code: 'additional_verification_required' }
    }
    const screen = await page.screen()
    const attention = {
      captcha: 'captcha_required', email_code: 'email_verification_required',
      consent: 'consent_required', additional: 'additional_verification_required',
    }[screen.kind]
    if (attention) return { status: 'needs_attention', error_code: attention }
    if (screen.kind === 'disabled') return { status: 'failed', error_code: 'account_unavailable' }
    if (screen.kind === 'invalid') return { status: 'failed', error_code: 'invalid_credentials' }
    if (['email', 'password', 'totp'].includes(screen.kind)) {
      if (submitted.has(screen.kind)) {
        // A successful submit can briefly leave the same form visible while it loads.
        if (previousKind !== screen.kind) {
          return { status: 'needs_attention', error_code: 'additional_verification_required' }
        }
      } else {
        if (screen.kind === 'totp' && !input.totp_secret) {
          return { status: 'needs_attention', error_code: 'additional_verification_required' }
        }
        const value = screen.kind === 'email' ? input.email : screen.kind === 'password' ? input.password : generateTOTP(input.totp_secret, now())
        await page.fillAndContinue(screen.kind, value)
        submitted.add(screen.kind)
        previousKind = screen.kind
        continue
      }
    }
    await wait()
  }
  return { status: 'needs_attention', error_code: 'login_timeout' }
}

const loginFields = {
  email: 'input[type="email"], input[name="username"], input[autocomplete="username"]',
  password: 'input[type="password"]',
  totp: 'input[autocomplete="one-time-code"], input[name="code"], input[name="otp"]',
}

export async function classifyLoginScreen(tab, blockedNavigation = false) {
  if (blockedNavigation) return { kind: 'additional' }
  const text = await tab.locator('body').innerText()
  const path = new URL(tab.url()).pathname
  const headings = (await tab.getByRole('heading').allTextContents()).join(' ')
  if (/(?:reset[-_/]?password|password[-_/](?:reset|change)|sign[-_]?up|enroll|create[-_]?account)/i.test(path) ||
      /(?:reset|change|choose|create|set)\b.{0,30}\bpassword|set up\b.{0,40}(?:authenticator|authentication|account)|create your account|重置密码|修改密码|设置.{0,10}(?:密码|验证器)/i.test(headings) ||
      await tab.locator('input[autocomplete="new-password"], input[name="new_password"], input[name="confirm_password"]').filter({ visible: true }).count() ||
      await tab.locator(loginFields.password).filter({ visible: true }).count() > 1) {
    return { kind: 'additional' }
  }
  if (await tab.locator('iframe[src*="challenges.cloudflare.com"], #challenge-running, #challenge-stage').filter({ visible: true }).count() || /verify you are human|complete the captcha|security check|人机验证/i.test(text)) return { kind: 'captcha' }
  if (/account (?:has been |is )?(?:deactivated|disabled|suspended)|account_deactivated|账号.{0,8}(?:停用|封禁)/i.test(text)) return { kind: 'disabled' }
  if (/incorrect password|wrong password|invalid (?:email or password|credentials)|密码错误/i.test(text)) return { kind: 'invalid' }
  if (/check your email|code.{0,40}(?:email|inbox)|(?:emailed|sent).{0,40}code|检查.{0,4}邮箱/i.test(text)) return { kind: 'email_code' }
  if (/wants (?:to access|access to)|allow .{0,40}access|authorize .{0,40}(?:app|codex)|授权.{0,10}访问/i.test(text) || await tab.getByRole('button', { name: /^(?:Authorize|Allow access|Allow|授权|允许)$/i }).filter({ visible: true }).count()) return { kind: 'consent' }
  for (const kind of ['password', 'email']) {
    if (await tab.locator(loginFields[kind]).filter({ visible: true }).count()) return { kind }
  }
  if (await tab.locator(loginFields.totp).filter({ visible: true }).count()) {
    return { kind: /authenticator|authentication app|验证器|身份验证应用/i.test(text) ? 'totp' : 'additional' }
  }
  return { kind: 'loading' }
}

export async function createIsolatedLoginContext(browser) {
  const context = await browser.newContext({ locale: 'en-US', serviceWorkers: 'block', acceptDownloads: false })
  // HTTP routing cannot intercept WebSocket handshakes. Ordinary OAuth forms
  // need no sockets, so close them before a page can contact another endpoint.
  await context.routeWebSocket('**/*', socket => socket.close())
  return context
}

// Only ordinary official login forms are automated. Unknown identity providers,
// CAPTCHAs, email codes, consent and security challenges require a human.
export async function browserLogin(input, signal) {
  const deadline = Date.now() + 150000
  if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
  const { chromium } = await import('playwright')
  if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true, timeout: 20000 })
  const stop = () => { void browser.close().catch(() => {}) }
  signal.addEventListener('abort', stop, { once: true })
  try {
    if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
    const context = await createIsolatedLoginContext(browser)
    const tab = await context.newPage()
    tab.setDefaultTimeout(5000)
    let callback, blockedNavigation = false
    const guard = {
      state: input.state, mainFrame: tab.mainFrame(), deadline, redirects: 0,
      setCallback: result => { callback = result },
      setBlocked: () => { blockedNavigation = true },
      navigate: async (url, from) => {
        // route.fulfill resolves before Chromium commits the original form's
        // navigation. Starting the next page sooner can be overwritten by it.
        await tab.waitForURL(from, { waitUntil: 'domcontentloaded', timeout: Math.max(1, Math.min(20000, deadline - Date.now())) })
        await tab.goto(url, { waitUntil: 'domcontentloaded', timeout: Math.max(1, Math.min(20000, deadline - Date.now())) })
      },
    }
    await context.route('**/*', async route => {
      try {
        await handleLoginRequest(route, guard)
      } catch {
        blockedNavigation = true
        await route.abort().catch(() => {})
      }
    })
    await tab.goto(input.authorization_url, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {
      // A validated redirect can replace the initial navigation before it finishes.
      if (!guard.redirects && !callback) blockedNavigation = true
    })
    const page = {
      url: () => callback ? `${callbackOrigin}${callbackPath}?${new URLSearchParams({ state: callback.state || '', code: callback.code || '' })}` : tab.url(),
      screen: () => classifyLoginScreen(tab, blockedNavigation),
      fillAndContinue: async (kind, value) => {
        if (new URL(tab.url()).origin !== loginOrigin) throw new Error('untrusted_login_origin')
        const field = tab.locator(loginFields[kind]).filter({ visible: true }).first()
        const action = await field.evaluate(element => element.form?.action || window.location.href)
        if (new URL(action).origin !== loginOrigin) throw new Error('untrusted_form_action')
        await field.fill(value)
        const button = tab.getByRole('button', { name: /^(?:Continue|Next|Sign in|Log in|Verify|Submit|继续|下一步|登录|验证)$/i }).filter({ visible: true }).first()
        await button.click()
        // Wait for this form to disappear before another classification; never resubmit it.
        await field.waitFor({ state: 'hidden', timeout: 15000 }).catch(() => {})
      },
    }
    const result = await completeLoginPage(page, input, signal, { deadline })
    return callback || result
  } finally {
    signal.removeEventListener('abort', stop)
    await browser.close()
  }
}
