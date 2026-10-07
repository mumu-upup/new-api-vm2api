import { setTimeout as delay } from 'node:timers/promises'
import { getLoginTOTP, LoginClock } from './login.mjs'

const callbackOrigin = 'http://localhost:1455'
const callbackPath = '/auth/callback'
const loginOrigin = 'https://auth.openai.com'
const codexConsentPath = '/sign-in-with-chatgpt/codex/consent'

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

function permittedRequest(url, method, mainNavigation, resourceType) {
  if (url.username || url.password || url.protocol !== 'https:') return false
  // The official SDK needs this exchange before a login form can submit.
  // Keep it limited to its exact endpoint and browser fetch/XHR requests;
  // credential forms, arbitrary writes and redirect replays remain blocked.
  if (!mainNavigation && ['fetch', 'xhr'].includes(resourceType) && method === 'POST' &&
      url.origin === 'https://sentinel.openai.com' && url.pathname === '/backend-api/sentinel/req' && !url.search) return true
  if (mainNavigation || !['GET', 'HEAD', 'OPTIONS'].includes(method)) return url.origin === loginOrigin
  // Keep credential submissions and top-level navigation restricted to auth.
  if (url.origin === 'https://sentinel.openai.com') return true
  return ['auth.openai.com', 'openai.com', 'oaistatic.com', 'cdn.auth0.com', 'challenges.cloudflare.com']
    .some(host => url.hostname === host || (host === 'oaistatic.com' && url.hostname.endsWith('.oaistatic.com')))
}

function requestDiagnostic(url, method, navigation, event, status) {
  // Never log arbitrary hosts, URL paths, query strings, headers, or bodies:
  // each can contain credentials, including on a blocked request.
  const destination = ['auth.openai.com', 'sentinel.openai.com', 'openai.com',
    'oaistatic.com', 'cdn.auth0.com', 'challenges.cloudflare.com'].includes(url.hostname)
    ? url.hostname : url.hostname.endsWith('.oaistatic.com') ? 'oaistatic.com' : 'other'
  return { event, destination,
    method: ['GET', 'POST', 'HEAD', 'OPTIONS', 'PUT', 'PATCH', 'DELETE'].includes(method) ? method : 'other',
    navigation, ...(status === undefined ? {} : { status }) }
}

async function recordCheckVerification(response, path, verification) {
  const passwordStep = path === '/api/accounts/password/verify'
  verification.totp = false
  if (passwordStep) verification.password = false
  // HTTP 200 alone is not authentication success. Require the official
  // API's next-page contract, then confirm the post-authentication screen.
  if (response.status() !== 200 || !/^application\/json(?:\s*;|$)/i.test(response.headers()['content-type'] || '')) return
  try {
    const body = await response.body()
    if (body.length > 32768) return
    const result = JSON.parse(body.toString('utf8'))
    if (typeof result?.continue_url !== 'string' || !result.continue_url ||
        Object.hasOwn(result, 'error') || result.success === false) return
    const next = new URL(result.continue_url, loginOrigin)
    if (next.origin !== loginOrigin || next.username || next.password || next.hash) return
    if (passwordStep) {
      verification.password = result.page?.type === 'mfa_challenge'
      return
    }
    verification.totp = verification.password && result.page?.type === 'sign_in_with_chatgpt_codex_consent' &&
      next.pathname === '/sign-in-with-chatgpt/codex/consent'
  } catch { /* Unknown responses never establish successful verification. */ }
}

export async function handleLoginRequest(route, guard) {
  const request = route.request()
  let url = new URL(request.url())
  const method = request.method()
  const resourceType = request.resourceType?.()
  const mainNavigation = request.isNavigationRequest() && request.frame() === guard.mainFrame
  for (let hop = 0; hop < 6; hop++) {
    const callback = parseCallback(url.href, guard.state)
    if (callback && mainNavigation && method === 'GET') {
      guard.setCallback(callback)
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Authorization received.' })
      return
    }
    if (!permittedRequest(url, method, mainNavigation, resourceType)) break
    // Playwright does not route Chromium's redirect hops. Fetch exactly one hop
    // and inspect its destination before any follow-up can carry credentials.
    let response
    const startedAt = guard.clock?.localNow()
    try {
      response = await route.fetch({ url: url.href, maxRedirects: 0, timeout: Math.max(1, Math.min(20000, guard.deadline - Date.now())) })
    } catch {
      guard.onDiagnostic?.(requestDiagnostic(url, method, mainNavigation, 'request_failed'))
      if (mainNavigation) guard.setBlocked('login_navigation_failed')
      await route.abort()
      return
    }
    const status = response.status()
    if (mainNavigation && url.origin === loginOrigin && status >= 200 && status < 400 && guard.clock) {
      const headers = response.headers()
      guard.clock.observe(headers.date, headers.age, startedAt, guard.clock.localNow())
    }
    if (guard.checkVerification && method === 'POST' && ['fetch', 'xhr'].includes(resourceType) &&
        request.frame() === guard.mainFrame && url.origin === loginOrigin && !url.search &&
        ['/api/accounts/password/verify', '/api/accounts/mfa/verify'].includes(url.pathname)) {
      await recordCheckVerification(response, url.pathname, guard.checkVerification)
    }
    if (status >= 400) guard.onDiagnostic?.(requestDiagnostic(url, method, mainNavigation, 'http_error', status))
    if (![301, 302, 303, 307, 308].includes(status)) {
      await route.fulfill({ response })
      return
    }
    if (mainNavigation && ++guard.redirects > 12) break
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
    if (!permittedRequest(next, nextMethod, mainNavigation, resourceType) || !['GET', 'HEAD'].includes(nextMethod)) break
    if (mainNavigation) {
      // Register the navigation wait before Chromium can commit this response,
      // including when a form POST uses the already-visible page URL.
      await Promise.all([
        guard.navigate(next.href, url.href),
        route.fulfill({ status: 200, contentType: 'text/plain', body: 'Continuing authorization.' }),
      ])
      return
    }
    // Preserve subresource semantics only for idempotent GET/HEAD requests.
    if (!['GET', 'HEAD'].includes(method) || next.origin !== url.origin) break
    url = next
  }
  guard.onDiagnostic?.(requestDiagnostic(url, method, mainNavigation, 'request_blocked'))
  if (mainNavigation) guard.setBlocked('login_navigation_blocked')
  await route.abort()
}

function verifiedCredentialCallback(callback, submitted, requireChallenges) {
  if (callback.status !== 'success' || !requireChallenges) return callback
  for (const kind of ['password', 'totp']) {
    if (!submitted.has(kind)) return { status: 'needs_attention', error_code: `${kind}_not_challenged` }
  }
  return callback
}

export async function completeLoginPage(page, input, signal, options = {}) {
  const now = options.now || Date.now
  const wait = options.wait || (() => delay(300, undefined, { signal }).catch(() => {}))
  const deadline = options.deadline || now() + 150000
  const submitted = new Set()
  let previousKind = ''
  let lastScreen = ''
  for (let step = 0; step < 500 && now() < deadline; step++) {
    if (signal.aborted) break
    const callback = parseCallback(page.url(), input.state)
    if (callback) return verifiedCredentialCallback(callback, submitted, options.checkOnly || options.probe)
    let screen
    try {
      screen = await page.screen()
    } catch (error) {
      if (signal.aborted) break
      if (!/execution context was destroyed|cannot find context with specified id/i.test(error?.message || '')) {
        return { status: 'needs_attention', error_code: 'login_screen_unrecognized' }
      }
      // A form navigation may replace the document between locator reads.
      screen = { kind: 'loading' }
    }
    if (signal.aborted || now() >= deadline) break
    // Redirect interception can finish while the document is being inspected.
    // Revalidate it before checking the origin of that now-replaced page.
    const completedCallback = parseCallback(page.url(), input.state)
    if (completedCallback) return verifiedCredentialCallback(completedCallback, submitted, options.checkOnly || options.probe)
    const diagnosticKind = ['loading', 'navigation_error', 'captcha', 'email_code', 'consent',
      'additional', 'workspace', 'invalid_totp', 'disabled', 'unavailable', 'invalid', 'email', 'password', 'totp'].includes(screen.kind)
      ? screen.kind : 'unknown'
    if (diagnosticKind !== lastScreen) {
      options.onDiagnostic?.({ event: 'screen', kind: diagnosticKind })
      lastScreen = diagnosticKind
    }
    if (screen.kind === 'navigation_error') {
      return { status: 'needs_attention', error_code: screen.error_code === 'login_navigation_failed' ? 'login_navigation_failed' : 'login_navigation_blocked' }
    }
    if (new URL(page.url()).origin !== loginOrigin) {
      return { status: 'needs_attention', error_code: 'additional_verification_required' }
    }
    if (options.checkOnly && ['workspace', 'consent'].includes(screen.kind) &&
        submitted.has('password') && submitted.has('totp') && page.credentialsVerified?.()) {
      return { status: 'credentials_verified', state: input.state }
    }
    if (options.probe && !options.checkOnly && ['workspace', 'consent'].includes(screen.kind)) {
      for (const kind of ['password', 'totp']) {
        if (!submitted.has(kind)) return { status: 'needs_attention', error_code: `${kind}_not_challenged` }
      }
    }
    if (screen.kind === 'workspace') {
      if (options.checkOnly || !input.expected_account_id && input.workspace_preference !== 'team_then_personal') {
        return { status: 'needs_attention', error_code: 'workspace_selection_required' }
      }
      if (submitted.has('workspace')) {
        if (previousKind !== 'workspace') return { status: 'needs_attention', error_code: 'additional_verification_required' }
      } else {
        try {
          await page.selectWorkspace(input.expected_account_id, input.workspace_preference)
        } catch (error) {
          if (signal.aborted) break
          return { status: 'needs_attention', error_code: /untrusted_(?:login_origin|form_action)/.test(error?.message || '')
            ? 'login_navigation_blocked' : 'workspace_selection_required' }
        }
        submitted.add('workspace')
        previousKind = 'workspace'
        continue
      }
    }
    if (screen.kind === 'consent' && (options.autoConsent || options.probe) && !options.checkOnly) {
      if (options.probe && (!submitted.has('password') || !submitted.has('totp'))) {
        return { status: 'needs_attention', error_code: !submitted.has('password') ? 'password_not_challenged' : 'totp_not_challenged' }
      }
      if (submitted.has('consent')) {
        if (previousKind !== 'consent') return { status: 'needs_attention', error_code: 'consent_required' }
      } else {
        try {
          await page.approveConsent()
        } catch (error) {
          if (signal.aborted) break
          return { status: 'needs_attention', error_code: /untrusted_(?:login_origin|form_action)/.test(error?.message || '')
            ? 'login_navigation_blocked' : 'consent_required' }
        }
        submitted.add('consent')
        previousKind = 'consent'
        continue
      }
      await wait()
      continue
    }
    const attention = {
      captcha: 'captcha_required', email_code: 'email_verification_required',
      consent: 'consent_required', additional: 'additional_verification_required',
      invalid_totp: 'invalid_totp_code',
    }[screen.kind]
    if (attention) return { status: 'needs_attention', error_code: attention }
    if (screen.kind === 'disabled') return { status: 'failed', error_code: 'account_disabled' }
    if (screen.kind === 'unavailable') return { status: 'failed', error_code: 'account_unavailable' }
    if (screen.kind === 'invalid') return { status: 'failed', error_code: 'invalid_credentials' }
    if (['email', 'password', 'totp'].includes(screen.kind)) {
      if (submitted.has(screen.kind)) {
        // A successful submit can briefly leave the same form visible while it loads.
        if (previousKind !== screen.kind) {
          return { status: 'needs_attention', error_code: 'additional_verification_required' }
        }
      } else {
        let value = screen.kind === 'email' ? input.email : input.password
        if (screen.kind === 'totp') {
          const totpInput = options.checkOnly || options.probe ? { ...input, totp_provider: 'local' } : input
          const result = await getLoginTOTP(totpInput, signal, { ...options, now: options.totpNow || options.now })
          if (!result.code) return result
          value = result.code
        }
        if (signal.aborted) break
        try {
          await page.fillAndContinue(screen.kind, value)
        } catch (error) {
          const callback = parseCallback(page.url(), input.state)
          if (callback) return verifiedCredentialCallback(callback, submitted, options.checkOnly || options.probe)
          if (signal.aborted) break
          return { status: 'needs_attention', error_code: /untrusted_(?:login_origin|form_action)/.test(error?.message || '')
            ? 'login_navigation_blocked' : `${screen.kind}_submission_failed` }
        }
        submitted.add(screen.kind)
        previousKind = screen.kind
        continue
      }
    }
    await wait()
  }
  return { status: 'needs_attention', error_code: signal.aborted ? 'login_timeout'
    : previousKind === 'consent' ? 'consent_required'
      : previousKind === 'workspace' ? 'workspace_selection_required'
        : previousKind ? `${previousKind}_step_timeout` : 'login_screen_unrecognized' }
}

const loginFields = {
  email: 'input[type="email"], input[name="username"], input[autocomplete="username"]',
  password: 'input[type="password"]',
  totp: 'input[autocomplete="one-time-code"], input[name="code"], input[name="otp"]',
}

export async function classifyLoginScreen(tab, navigationError = '') {
  if (navigationError) return { kind: 'navigation_error', error_code: navigationError }
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
  const workspaceRadios = tab.locator('input[type="radio"][name="workspace_id"]').filter({ visible: true })
  if (await workspaceRadios.count()) return { kind: 'workspace' }
  // Generic selector wording without a concrete official control remains a
  // manual handoff. It must never authorize a click by itself.
  if (/(?:choose|select)\s+(?:(?:a|an)\s+)?(?:workspace|account|organization|team)|(?:workspace|organization)\s+(?:selection|selector)|选择.{0,8}(?:工作空间|账户|组织|团队)/i.test(`${headings} ${text}`)) {
    return { kind: 'additional' }
  }
  if (/account (?:has been |is )?(?:deactivated|disabled|suspended)|account_deactivated|账号.{0,8}(?:停用|封禁)/i.test(text)) return { kind: 'disabled' }
  if (/account (?:has been |is )?(?:temporarily )?(?:unavailable|not available|deleted)|account_(?:unavailable|deleted)|账号.{0,8}(?:不可用|已删除)/i.test(text)) return { kind: 'unavailable' }
  if (/incorrect password|wrong password|invalid (?:email or password|credentials)|密码错误/i.test(text)) return { kind: 'invalid' }
  const emailCode = /check your email|code.{0,40}(?:email|inbox)|(?:emailed|sent).{0,40}code|检查.{0,4}邮箱/i
  const authenticator = /authenticator|authentication app|验证器|身份验证应用/i
  const hasOTP = await tab.locator(loginFields.totp).filter({ visible: true }).count()
  // Alternative sign-in links must not override the active challenge heading.
  if (emailCode.test(headings)) return { kind: 'email_code' }
  if (hasOTP && authenticator.test(text) && /(?:incorrect|invalid|expired)\s+(?:(?:authentication|authenticator|verification|security|one[- ]time)\s+)?code|code (?:is |has )?(?:invalid|incorrect|expired)|验证码.{0,5}(?:错误|不正确|无效|过期)/i.test(text)) return { kind: 'invalid_totp' }
  if (hasOTP && authenticator.test(headings)) return { kind: 'totp' }
  if (emailCode.test(text)) return { kind: 'email_code' }
  if (path === codexConsentPath) return { kind: 'consent' }
  if (/wants (?:to access|access to)|allow .{0,40}access|authorize .{0,40}(?:app|codex)|授权.{0,10}访问/i.test(text) || await tab.getByRole('button', { name: /^(?:Authorize|Allow access|Allow|授权|允许)$/i }).filter({ visible: true }).count()) return { kind: 'consent' }
  for (const kind of ['password', 'email']) {
    if (await tab.locator(loginFields[kind]).filter({ visible: true }).count()) return { kind }
  }
  if (hasOTP) {
    return { kind: authenticator.test(text) ? 'totp' : 'additional' }
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
// CAPTCHAs, email codes and security challenges require a human. Login and
// explicit model probes may submit the observed official Codex consent form.
export async function browserLogin(input, signal, options = {}) {
  const deadline = Date.now() + 150000
  const clock = new LoginClock(options.now || Date.now)
  const onDiagnostic = options.onDiagnostic || (event => console.info(JSON.stringify({ source: 'gpt_login', ...event })))
  if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
  const { chromium } = await import('playwright')
  if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
  const browser = await (options.launch || chromium.launch.bind(chromium))({ headless: true, chromiumSandbox: true, timeout: 20000 })
  const stop = () => { void browser.close().catch(() => {}) }
  signal.addEventListener('abort', stop, { once: true })
  try {
    if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
    const context = await createIsolatedLoginContext(browser)
    const tab = await context.newPage()
    tab.setDefaultTimeout(5000)
    let callback, navigationError = '', pendingNavigations = 0
    const guard = {
      state: input.state, mainFrame: tab.mainFrame(), deadline, redirects: 0, clock,
      checkVerification: options.checkOnly ? { password: false, totp: false } : null,
      onDiagnostic,
      setCallback: result => { callback = result },
      setBlocked: code => { navigationError ||= code },
      navigate: async (url, from) => {
        // A URL equality check can match the old document when a form posts to
        // its own URL. Wait for the actual new document before following.
        await tab.waitForEvent('framenavigated', { predicate: frame => frame === tab.mainFrame() && frame.url() === from,
          timeout: Math.max(1, Math.min(20000, deadline - Date.now())) })
        await tab.goto(url, { waitUntil: 'domcontentloaded', timeout: Math.max(1, Math.min(20000, deadline - Date.now())) })
      },
    }
    await context.route('**/*', async route => {
      const request = route.request()
      const mainNavigation = request.isNavigationRequest() && request.frame() === guard.mainFrame
      if (mainNavigation) pendingNavigations++
      try {
        await handleLoginRequest(route, guard)
      } catch {
        if (mainNavigation) guard.setBlocked('login_navigation_failed')
        await route.abort().catch(() => {})
      } finally {
        if (mainNavigation) pendingNavigations--
      }
    })
    await tab.goto(input.authorization_url, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {
      // A validated redirect can replace the initial navigation before it finishes.
      if (!guard.redirects && !callback) guard.setBlocked('login_navigation_failed')
    })
    const page = {
      credentialsVerified: () => guard.checkVerification?.password && guard.checkVerification?.totp,
      url: () => callback ? `${callbackOrigin}${callbackPath}?${new URLSearchParams({ state: callback.state || '', code: callback.code || '' })}` : tab.url(),
      screen: () => pendingNavigations && !navigationError ? { kind: 'loading' } : classifyLoginScreen(tab, navigationError),
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
      selectWorkspace: async (expectedAccountID, workspacePreference) => {
        if (new URL(tab.url()).origin !== loginOrigin) throw new Error('untrusted_login_origin')
        if (options.probe && new URL(tab.url()).pathname !== codexConsentPath) throw new Error('untrusted_form_action')
        const result = await tab.evaluate(({ expected, preference, origin, probe, consentPath }) => {
          const visible = element => {
            const style = window.getComputedStyle(element)
            return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
          }
          const radios = [...document.querySelectorAll('input[type="radio"][name="workspace_id"]')]
            .filter(visible)
          let matches = []
          if (expected) {
            matches = radios.filter(element => element.value === expected)
          } else if (preference === 'team_then_personal') {
            // Official workspace radios label the personal account explicitly;
            // organization workspaces have user-defined names (not necessarily
            // containing "Team"). Never infer a selection from checked order.
            const choices = radios.map(element => ({ element, label: [...element.labels || []]
              .filter(visible).map(label => label.innerText.trim()).join(' ').trim() }))
            if (!choices.length || choices.some(choice => !choice.label || !choice.element.value)) return { ok: false, error: 'workspace_selection_required' }
            const personal = /^(?:personal(?: account| workspace)?|个人(?:账户|帐户|账号|空间|工作区)|個人(?:帳戶|賬戶|帳號|空間|工作區))$/i
            const teams = choices.filter(choice => {
              if (personal.test(choice.label)) return false
              // The official English label may include its "Pa" avatar on a
              // separate line. Other prefixes or extra text remain ambiguous.
              const lines = choice.label.split(/\r?\n/).map(line => line.trim())
              return !(lines.length === 2 && /^Pa$/i.test(lines[0]) && /^Personal account$/i.test(lines[1]))
            })
            matches = (teams.length ? teams : choices).map(choice => choice.element)
          }
          if (matches.length !== 1) return { ok: false, error: 'workspace_selection_required' }
          const radio = matches[0]
          if (radio.disabled || radios.filter(element => element.value === radio.value).length !== 1) return { ok: false, error: 'workspace_selection_required' }
          const form = radio.form
          if (!form || form.method.toLowerCase() !== 'post') return { ok: false, error: 'workspace_selection_required' }
          if (probe && ([...document.forms].filter(visible).length !== 1 ||
              [...form.querySelectorAll('input:not([type="hidden"]):not([type="radio"][name="workspace_id"]), select, textarea')].some(visible))) {
            return { ok: false, error: 'workspace_selection_required' }
          }
          let action
          try { action = new URL(form.getAttribute('action') || window.location.href, window.location.href) } catch { return { ok: false, error: 'workspace_selection_required' } }
          if (action.origin !== origin || form.target && form.target !== '_self') return { ok: false, error: 'untrusted_form_action' }
          if (probe && (action.pathname !== consentPath || action.username || action.password || action.search || action.hash)) return { ok: false, error: 'untrusted_form_action' }
          const submitters = [...form.querySelectorAll('button, input[type="submit"]')]
            .filter(element => visible(element) && !element.disabled &&
              (!probe || element.type === 'submit' && !element.matches(':disabled') && !element.closest('[aria-disabled="true"]')) &&
              (probe ? /^(?:Continue|继续|繼續)$/ : /^(?:continue|next|submit|继续|下一步)$/i)
                .test((element.textContent || element.value || element.getAttribute('aria-label') || '').trim()))
          if (submitters.length !== 1) return { ok: false, error: 'workspace_selection_required' }
          const submitter = submitters[0]
          if (['formaction', 'formmethod', 'formtarget'].some(attribute => submitter.hasAttribute(attribute))) return { ok: false, error: 'untrusted_form_action' }
          radio.checked = true
          radio.dispatchEvent(new Event('input', { bubbles: true }))
          radio.dispatchEvent(new Event('change', { bubbles: true }))
          if (!radio.checked || radio.form !== form) return { ok: false, error: 'workspace_selection_required' }
          form.requestSubmit(submitter)
          return { ok: true }
        }, { expected: expectedAccountID, preference: workspacePreference, origin: loginOrigin,
          probe: Boolean(options.probe || options.autoConsent && new URL(tab.url()).pathname === codexConsentPath), consentPath: codexConsentPath })
        if (!result.ok) throw new Error(result.error)
      },
      approveConsent: async () => {
        if (!(options.autoConsent || options.probe) || options.checkOnly) throw new Error('consent_required')
        const url = new URL(tab.url())
        if (url.origin !== loginOrigin || url.pathname !== codexConsentPath || url.username || url.password || url.hash) throw new Error('untrusted_form_action')
        const result = await tab.evaluate(({ origin, path }) => {
          const visible = element => {
            const style = window.getComputedStyle(element)
            return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
          }
          const forms = [...document.forms].filter(visible)
          if (forms.length !== 1) return { ok: false, error: 'consent_required' }
          const form = forms[0]
          if (form.method.toLowerCase() !== 'post') return { ok: false, error: 'consent_required' }
          let action
          try { action = new URL(form.getAttribute('action') || window.location.href, window.location.href) } catch { return { ok: false, error: 'consent_required' } }
          if (action.origin !== origin || action.pathname !== path || action.username || action.password || action.search || action.hash ||
              form.target && form.target !== '_self') return { ok: false, error: 'untrusted_form_action' }
          // The observed form uses one Continue submitter. Unknown interactive
          // fields can be a security challenge; leave those to a person.
          if ([...form.querySelectorAll('input:not([type="hidden"]), select, textarea')].some(visible)) return { ok: false, error: 'consent_required' }
          const submitters = [...form.querySelectorAll('button, input[type="submit"]')]
            .filter(element => visible(element) && !element.matches(':disabled') && !element.closest('[aria-disabled="true"]') && element.type === 'submit' &&
              /^(?:Continue|继续|繼續)$/.test((element.textContent || element.value || element.getAttribute('aria-label') || '').trim()))
          if (submitters.length !== 1) return { ok: false, error: 'consent_required' }
          const submitter = submitters[0]
          if (['formaction', 'formmethod', 'formtarget'].some(attribute => submitter.hasAttribute(attribute))) return { ok: false, error: 'untrusted_form_action' }
          form.requestSubmit(submitter)
          return { ok: true }
        }, { origin: loginOrigin, path: codexConsentPath })
        if (!result.ok) throw new Error(result.error)
      },
    }
    const result = await completeLoginPage(page, input, signal, { ...options, deadline, onDiagnostic, totpNow: () => clock.now() })
    if (options.checkOnly || options.probe) return result
    return callback || (navigationError ? { status: 'needs_attention', error_code: navigationError } : result)
  } finally {
    signal.removeEventListener('abort', stop)
    await browser.close()
  }
}
