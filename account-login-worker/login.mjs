import { createHmac } from 'node:crypto'

const oauthClientID = 'app_EMoamEEZ73f0CkXaXp7hrann'
const callbackURL = 'http://localhost:1455/auth/callback'
const errorCodes = new Set([
  'captcha_required', 'email_verification_required', 'consent_required',
  'additional_verification_required', 'invalid_credentials', 'account_unavailable',
  'login_timeout', 'automation_unavailable', 'invalid_callback',
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

export function validateLoginRequest(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('invalid_request')
  const { authorization_url, state, email, password, totp_secret = '' } = input
  if (typeof authorization_url !== 'string' || authorization_url.length > 4096 ||
      typeof state !== 'string' || !/^[A-Za-z0-9_-]{24,128}$/.test(state) ||
      typeof email !== 'string' || email.length > 320 || !/^[^\s@]+@[^\s@]+$/.test(email) ||
      typeof password !== 'string' || !password || password.length > 1024 ||
      typeof totp_secret !== 'string') throw new Error('invalid_request')
  const url = new URL(authorization_url)
  if (url.origin !== 'https://auth.openai.com' || url.pathname !== '/oauth/authorize' ||
      url.username || url.password || url.hash ||
      url.searchParams.get('client_id') !== oauthClientID ||
      url.searchParams.get('response_type') !== 'code' ||
      url.searchParams.get('redirect_uri') !== callbackURL ||
      url.searchParams.get('state') !== state ||
      url.searchParams.get('code_challenge_method') !== 'S256' ||
      !/^[A-Za-z0-9_-]{43}$/.test(url.searchParams.get('code_challenge') || '')) throw new Error('invalid_request')
  for (const key of ['client_id', 'response_type', 'redirect_uri', 'state', 'code_challenge', 'code_challenge_method']) {
    if (url.searchParams.getAll(key).length !== 1) throw new Error('invalid_request')
  }
  if (totp_secret) decodeSeed(totp_secret).fill(0)
  return { authorization_url, state, email, password, totp_secret }
}

export function safeLoginResult(result, state) {
  if (result?.status === 'success') {
    if (result.state === state && typeof result.code === 'string' && /^[\x21-\x7e]{1,4096}$/.test(result.code)) {
      return { status: 'success', code: result.code, state }
    }
    return { status: 'failed', error_code: 'invalid_callback' }
  }
  return {
    status: result?.status === 'needs_attention' ? 'needs_attention' : 'failed',
    error_code: errorCodes.has(result?.error_code) ? result.error_code : 'automation_unavailable',
  }
}
