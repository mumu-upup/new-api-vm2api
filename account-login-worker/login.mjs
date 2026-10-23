import { createHmac } from 'node:crypto'

const oauthProfiles = [
  { clientID: 'app_EMoamEEZ73f0CkXaXp7hrann', authorizePath: '/oauth/authorize',
    callbackURL: 'http://localhost:1455/auth/callback', statePattern: /^[A-Za-z0-9_-]{24,128}$/ },
  { clientID: 'app_fnr0pYvVwwFDocDumLG3H2Bp', authorizePath: '/api/accounts/authorize',
    callbackURL: 'https://bps.openai.com/basispoints/extension/360590d7-f8f9-4d88-bf75-0edfe0a4b9f3/auth/callback',
    statePattern: /^bps\.[A-Za-z0-9_-]{24,128}\.PC$/ },
]

export function loginOAuthProfile(url) {
  if (url.origin !== 'https://auth.openai.com' || url.username || url.password || url.hash) return undefined
  return oauthProfiles.find(profile => url.pathname === profile.authorizePath &&
    url.searchParams.get('client_id') === profile.clientID &&
    url.searchParams.get('redirect_uri') === profile.callbackURL)
}
const errorCodes = new Set([
  'captcha_required', 'email_verification_required', 'consent_required',
  'additional_verification_required', 'invalid_credentials', 'account_disabled', 'account_unavailable',
  'login_timeout', 'automation_unavailable', 'invalid_callback',
  'login_navigation_blocked', 'login_navigation_failed', 'login_screen_unrecognized',
  'email_submission_failed', 'password_submission_failed', 'totp_submission_failed',
  'email_step_timeout', 'password_step_timeout', 'totp_step_timeout',
  'workspace_selection_required', 'password_not_challenged', 'totp_not_challenged',
  'totp_secret_required', 'totp_provider_invalid_response',
  'totp_provider_unavailable', 'totp_provider_timeout', 'invalid_totp_code',
  'session_cleanup_unverified',
])

function decodeSeed(value) {
  if (typeof value !== 'string' || value.length > 256) throw new Error('invalid_totp_secret')
  const normalized = value.replace(/\s/g, '').toUpperCase().replace(/=+$/, '')
  if (!/^[A-Z2-7]{16,128}$/.test(normalized)) throw new Error('invalid_totp_secret')
  const bytes = []
  let buffer = 0, bits = 0
  for (const character of normalized) {
    buffer = (buffer << 5) | 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(character)
    bits += 5
    if (bits >= 8) {
      bits -= 8
      bytes.push((buffer >>> bits) & 255)
    }
  }
  if (bits && (buffer & ((1 << bits) - 1))) throw new Error('invalid_totp_secret')
  return Buffer.from(bytes)
}

export function generateTOTP(seed, timestamp = Date.now()) {
  const key = decodeSeed(seed)
  try {
    const counter = Buffer.alloc(8)
    counter.writeBigUInt64BE(BigInt(Math.floor(timestamp / 30000)))
    const digest = createHmac('sha1', key).update(counter).digest()
    const offset = digest[digest.length - 1] & 15
    return String((digest.readUInt32BE(offset) & 0x7fffffff) % 1000000).padStart(6, '0')
  } finally {
    key.fill(0)
  }
}

export class LoginClock {
  constructor(localNow = Date.now) {
    this.localNow = localNow
    this.offsetMs = 0
    this.bestRoundTripMs = Infinity
  }

  observe(date, age, startedAt, receivedAt) {
    // Use only fresh, low-latency samples. Callers restrict the source to
    // successful HTTPS navigations on the official authentication origin.
    if (typeof date !== 'string' || age !== undefined && age !== '0') return
    const serverTime = Date.parse(date)
    const roundTripMs = receivedAt - startedAt
    if (!Number.isFinite(serverTime) || !Number.isFinite(roundTripMs) || roundTripMs < 0 ||
        roundTripMs > 5000 || roundTripMs > this.bestRoundTripMs) return
    this.offsetMs = serverTime - (startedAt + receivedAt) / 2
    this.bestRoundTripMs = roundTripMs
  }

  now() {
    return this.localNow() + this.offsetMs
  }
}

export async function getLoginTOTP(input, signal, options = {}) {
  if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
  if (!input.totp_secret) return { status: 'needs_attention', error_code: 'totp_secret_required' }
  if (!input.totp_provider || input.totp_provider === 'local') {
    return { code: generateTOTP(input.totp_secret, (options.now || Date.now)()) }
  }
  if (input.totp_provider !== '2fa_cn') {
    return { status: 'needs_attention', error_code: 'totp_provider_invalid_response' }
  }
  decodeSeed(input.totp_secret).fill(0)
  const seed = input.totp_secret.replace(/\s/g, '').toUpperCase().replace(/=+$/, '')
  const timeout = new AbortController()
  const timer = setTimeout(() => timeout.abort(), options.providerTimeoutMs ?? 5000)
  const providerSignal = AbortSignal.any([signal, timeout.signal])
  const invalid = { status: 'needs_attention', error_code: 'totp_provider_invalid_response' }
  let response
  try {
    // Explicit opt-in only. No cookies, worker token, configurable host, or
    // redirect can carry the seed to a second destination.
    response = await (options.fetch || fetch)(`https://2fa.cn/codes/${seed}`, {
      method: 'GET', redirect: 'error', cache: 'no-store', credentials: 'omit',
      headers: { Accept: 'application/json' }, signal: providerSignal,
    })
    if ((response.status >= 300 && response.status < 400) || response.redirected) return invalid
    if (!response.ok) return { status: 'failed', error_code: 'totp_provider_unavailable' }
    if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get('content-type') || '') || !response.body) return invalid
    if (Number(response.headers.get('content-length')) > 4096) return invalid
    const reader = response.body.getReader()
    const chunks = []
    let size = 0
    try {
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        size += value.byteLength
        if (size > 4096) return invalid
        chunks.push(value)
      }
    } finally {
      await reader.cancel().catch(() => {})
      reader.releaseLock()
    }
    let payload
    try {
      payload = JSON.parse(Buffer.concat(chunks).toString('utf8'))
    } catch {
      return invalid
    }
    if (!payload || typeof payload.code !== 'string' || !/^\d{6}$/.test(payload.code) || payload.ostring !== seed) return invalid
    providerSignal.throwIfAborted()
    return { code: payload.code }
  } catch {
    if (signal.aborted) return { status: 'needs_attention', error_code: 'login_timeout' }
    return { status: 'failed', error_code: timeout.signal.aborted ? 'totp_provider_timeout' : 'totp_provider_unavailable' }
  } finally {
    clearTimeout(timer)
    if (response?.body && !response.body.locked) await response.body.cancel().catch(() => {})
  }
}

export function validateLoginRequest(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('invalid_request')
  const { authorization_url, state, email, password, totp_secret = '', totp_provider = 'local', expected_account_id = '', workspace_preference = '' } = input
  if (typeof authorization_url !== 'string' || authorization_url.length > 4096 ||
      typeof state !== 'string' || state.length > 135 ||
      typeof email !== 'string' || email.length > 320 || !/^[^\s@]+@[^\s@]+$/.test(email) ||
      typeof password !== 'string' || !password || password.length > 1024 ||
      typeof totp_secret !== 'string' || !['', 'local', '2fa_cn'].includes(totp_provider) ||
      !['', 'team_then_personal'].includes(workspace_preference) ||
      typeof expected_account_id !== 'string' || expected_account_id.length > 128 || /[\x00-\x20\x7f]/.test(expected_account_id)) throw new Error('invalid_request')
  const url = new URL(authorization_url)
  const profile = loginOAuthProfile(url)
  if (!profile || !profile.statePattern.test(state) ||
      url.searchParams.get('response_type') !== 'code' ||
      url.searchParams.get('state') !== state ||
      url.searchParams.get('code_challenge_method') !== 'S256' ||
      !/^[A-Za-z0-9_-]{43}$/.test(url.searchParams.get('code_challenge') || '')) throw new Error('invalid_request')
  for (const key of ['client_id', 'response_type', 'redirect_uri', 'state', 'code_challenge', 'code_challenge_method']) {
    if (url.searchParams.getAll(key).length !== 1) throw new Error('invalid_request')
  }
  if (totp_secret) decodeSeed(totp_secret).fill(0)
  return { authorization_url, state, email, password, totp_secret, totp_provider: totp_provider || 'local', expected_account_id, workspace_preference }
}

export function safeLoginResult(result, state) {
  if (result?.status === 'success') {
    if (result.state === state && typeof result.code === 'string' && /^[\x21-\x7e]{1,4096}$/.test(result.code)) {
      if (Object.hasOwn(result, 'session_cleanup')) {
        const cleanup = result.session_cleanup
        if (cleanup?.status !== 'success' || !Number.isInteger(cleanup.loggedOut) || cleanup.loggedOut < 0 || cleanup.loggedOut > 100) {
          return { status: 'needs_attention', error_code: 'session_cleanup_unverified' }
        }
        return { status: 'success', code: result.code, state,
          session_cleanup: { status: 'success', loggedOut: cleanup.loggedOut } }
      }
      return { status: 'success', code: result.code, state }
    }
    return { status: 'failed', error_code: 'invalid_callback' }
  }
  return {
    status: result?.status === 'needs_attention' ? 'needs_attention' : 'failed',
    error_code: errorCodes.has(result?.error_code) ? result.error_code : 'automation_unavailable',
  }
}

export function safeCheckResult(result, state) {
  if (result?.status === 'credentials_verified') {
    return result.state === state ? { status: 'success' } : { status: 'failed', error_code: 'invalid_callback' }
  }
  const sanitized = safeLoginResult(result, state)
  return sanitized.status === 'success' ? { status: 'success' } : sanitized
}
