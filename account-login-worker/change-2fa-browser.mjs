import { chromium } from 'playwright'
import { cleanupActiveSessions } from './session-cleanup.mjs'
import { generateTOTP } from './login.mjs'

const authOrigin = 'https://auth.openai.com'
const chatOrigin = 'https://chatgpt.com'
const moyanovoOrigin = 'https://moyanovo.space'

function allowed(url, method) {
  if (url.protocol !== 'https:' || url.username || url.password || url.hash) return false
  if ([authOrigin, chatOrigin, moyanovoOrigin].includes(url.origin)) return true
  return method === 'GET' && ['openai.com', 'oaistatic.com', 'cdn.auth0.com', 'challenges.cloudflare.com'].some(host =>
    url.hostname === host || url.hostname.endsWith(`.${host}`))
}

async function contextFor(browser, origins) {
  const context = await browser.newContext({ locale: 'en-US', serviceWorkers: 'block', acceptDownloads: false })
  await context.routeWebSocket('**/*', socket => socket.close())
  await context.route('**/*', async route => {
    const request = route.request()
    const url = new URL(request.url())
    if (origins.includes(url.origin) && allowed(url, request.method())) return route.continue()
    if (allowed(url, request.method())) return route.continue()
    await route.abort()
  })
  return context
}

async function visibleLocator(page, selectors) {
  for (const selector of selectors) {
    const locator = page.locator(selector).filter({ visible: true }).first()
    if (await locator.count()) return locator
  }
  return null
}

async function submitLogin(page, input, signal) {
  const email = await visibleLocator(page, ['input[type="email"]', 'input[name="email"]'])
  if (!email) throw new Error('login_screen_unrecognized')
  await email.fill(input.email)
  const next = await visibleLocator(page, ['button:has-text("Continue")', 'button:has-text("Next")', 'button:has-text("继续")'])
  if (!next) throw new Error('login_screen_unrecognized')
  await next.click()
  const password = await visibleLocator(page, ['input[type="password"]', 'input[name="password"]'])
  if (!password) throw new Error('password_submission_failed')
  await password.fill(input.password)
  const signIn = await visibleLocator(page, ['button:has-text("Continue")', 'button:has-text("Sign in")', 'button:has-text("Log in")'])
  if (!signIn) throw new Error('password_submission_failed')
  await signIn.click()
  const otp = await visibleLocator(page, ['input[autocomplete="one-time-code"]', 'input[name="code"]', 'input[name="otp"]'])
  if (otp) {
    await otp.fill(generateTOTP(input.totpSecret))
    const verify = await visibleLocator(page, ['button:has-text("Continue")', 'button:has-text("Verify")', 'button:has-text("验证")'])
    if (!verify) throw new Error('totp_submission_failed')
    await verify.click()
  }
  await page.waitForURL(url => url.origin === chatOrigin, { timeout: 20000 }).catch(() => {})
  signal.throwIfAborted()
  if (new URL(page.url()).origin !== chatOrigin) throw new Error('additional_verification_required')
}

async function verifyReplacementLogin(input, secret, signal) {
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true, timeout: 20000 })
  try {
    const context = await contextFor(browser, [authOrigin, chatOrigin])
    const page = await context.newPage()
    page.setDefaultTimeout(7000)
    await page.goto(`${chatOrigin}/auth/login`, { waitUntil: 'domcontentloaded', timeout: 20000 })
    await submitLogin(page, { email: input.email, password: input.password, totpSecret: secret }, signal)
    return true
  } catch {
    return false
  } finally {
    await browser.close().catch(() => {})
  }
}

function extractSecret(text) {
  const uri = text.match(/otpauth:\/\/totp\/[^\s"'<>]+/i)
  let candidate = ''
  if (uri) {
    try { candidate = new URL(uri[0]).searchParams.get('secret') || '' } catch { candidate = '' }
  }
  if (!candidate) {
    const match = text.match(/(?:secret|key|密钥)[^A-Z2-7]{0,32}([A-Z2-7]{16,128})/i)
    candidate = match?.[1] || ''
  }
  candidate = candidate.toUpperCase().replace(/=+$/, '')
  try { generateTOTP(candidate, 0); return candidate } catch { return '' }
}

async function primaryBrowserChange(input, signal) {
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true, timeout: 20000 })
  try {
    const context = await contextFor(browser, [authOrigin, chatOrigin])
    const page = await context.newPage()
    page.setDefaultTimeout(7000)
    await page.goto(`${chatOrigin}/auth/login`, { waitUntil: 'domcontentloaded', timeout: 20000 })
    await submitLogin(page, { email: input.email, password: input.password, totpSecret: input.oldTotpSecret }, signal)
    await page.goto(`${chatOrigin}/#settings/Security`, { waitUntil: 'domcontentloaded', timeout: 20000 })
    const start = await visibleLocator(page, [
      'button:has-text("Authenticator app")', 'button:has-text("Set up")', 'button:has-text("Enable two-factor")',
      '[role="button"]:has-text("Authenticator app")', '[role="button"]:has-text("Set up")',
    ])
    if (!start) throw new Error('security_flow_unavailable')
    await start.click()
    const body = await page.locator('body').innerText({ timeout: 5000 })
    const newSecret = extractSecret(body)
    if (!newSecret) throw new Error('security_flow_unavailable')
    const code = await visibleLocator(page, ['input[autocomplete="one-time-code"]', 'input[name="code"]', 'input[name="otp"]'])
    if (!code) throw new Error('security_flow_unavailable')
    await code.fill(generateTOTP(newSecret))
    const confirm = await visibleLocator(page, ['button:has-text("Continue")', 'button:has-text("Confirm")', 'button:has-text("Enable")', 'button:has-text("Verify")'])
    if (!confirm) throw new Error('security_flow_unavailable')
    await confirm.click()
    await page.waitForTimeout(500)
    await page.goto(`${chatOrigin}/#settings/Security`, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {})
    if (!(await page.locator('body').innerText()).match(/two.factor|authenticator|enabled/i) ||
        !(await verifyReplacementLogin(input, newSecret, signal))) throw new Error('replacement_unverified')
    await cleanupActiveSessions(page, signal, { deadline: Date.now() + 15000 }).catch(() => {})
    return { status: 'success', new_totp_secret: newSecret, verified: true }
  } catch (error) {
    const code = error?.message
    const terminal = ['invalid_credentials', 'account_disabled', 'email_verification_required', 'additional_verification_required'].includes(code)
    return { status: 'failed', error_code: terminal ? code : 'security_flow_unavailable', recoverable: !terminal }
  } finally {
    await browser.close().catch(() => {})
  }
}

async function moyanovoBrowserChange(input, signal) {
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true, timeout: 20000 })
  try {
    const context = await contextFor(browser, [moyanovoOrigin])
    const page = await context.newPage()
    page.setDefaultTimeout(7000)
    await page.goto(`${moyanovoOrigin}/`, { waitUntil: 'domcontentloaded', timeout: 20000 })
    const fields = await page.locator('input').filter({ visible: true }).all()
    if (fields.length < 3) throw new Error('security_flow_unavailable')
    await fields[0].fill(input.email)
    await fields[1].fill(input.password)
    await fields[2].fill(input.oldTotpSecret)
    const force = page.getByText('强制跳过邮箱验证', { exact: true }).locator('..').locator('input[type="checkbox"]')
    if (await force.count() && await force.isChecked()) await force.uncheck()
    const start = await visibleLocator(page, ['button:has-text("开始换绑与重新重登")', 'button:has-text("2FA修改")'])
    if (!start) throw new Error('security_flow_unavailable')
    await start.click()
    await page.waitForTimeout(1000)
    const secret = extractSecret(await page.locator('body').innerText())
    if (!secret) throw new Error('security_flow_unavailable')
    signal.throwIfAborted()
    if (!(await verifyReplacementLogin(input, secret, signal))) throw new Error('replacement_unverified')
    return { status: 'success', new_totp_secret: secret, verified: true }
  } catch {
    return { status: 'failed', error_code: 'automation_unavailable' }
  } finally {
    await browser.close().catch(() => {})
  }
}

export { primaryBrowserChange, moyanovoBrowserChange }
