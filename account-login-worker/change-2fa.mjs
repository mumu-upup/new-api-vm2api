import { generateTOTP } from './login.mjs'

const errorCodes = new Set([
  'change_timeout', 'automation_unavailable', 'security_flow_unavailable',
  'replacement_unverified', 'replacement_invalid', 'credentials_required',
  'email_verification_required', 'additional_verification_required',
  'invalid_credentials', 'account_disabled', 'account_unavailable',
])

function normalizeSecret(value) {
  if (typeof value !== 'string' || value.length > 512) throw new Error('invalid_totp_secret')
  let candidate = value.trim()
  if (/^otpauth:\/\//i.test(candidate)) {
    const url = new URL(candidate)
    if (url.protocol !== 'otpauth:' || url.hostname.toLowerCase() !== 'totp' ||
        url.username || url.password || url.hash || url.searchParams.getAll('secret').length !== 1) {
      throw new Error('invalid_totp_secret')
    }
    candidate = url.searchParams.get('secret') || ''
  }
  candidate = candidate.replace(/\s/g, '').toUpperCase().replace(/=+$/, '')
  // generateTOTP performs the same strict Base32 validation used by login.
  generateTOTP(candidate, 0)
  return candidate
}

export function validateChangeRequest(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('invalid_request')
  const { email, password, old_totp_secret: oldTotpSecret } = input
  if (typeof email !== 'string' || email.length > 320 || !/^[^\s@]+@[^\s@]+$/.test(email) ||
      typeof password !== 'string' || !password || password.length > 1024 ||
      typeof oldTotpSecret !== 'string') throw new Error('invalid_request')
  try { normalizeSecret(oldTotpSecret) } catch { throw new Error('invalid_request') }
  return { email, password, old_totp_secret: normalizeSecret(oldTotpSecret) }
}

function adapterSecret(result) {
  if (!result || result.status !== 'success' || result.verified !== true) return null
  try { return normalizeSecret(result.new_totp_secret) } catch { return null }
}

function failure(stage, fallbackUsed, errorCode) {
  return { status: 'failed', stage, fallback_used: fallbackUsed, error_code: errorCodes.has(errorCode) ? errorCode : 'automation_unavailable' }
}

export function safeChangeResult(result) {
  const stage = result?.stage === 'fallback' ? 'fallback' : 'primary'
  const fallbackUsed = Boolean(result?.fallback_used)
  if (result?.status === 'success') {
    const secret = adapterSecret(result)
    if (secret) return { status: 'success', stage, fallback_used: fallbackUsed, new_totp_secret: secret }
    return failure(stage, fallbackUsed, 'replacement_invalid')
  }
  return failure(stage, fallbackUsed, result?.error_code)
}

function isRecoverable(result) {
  return result?.status === 'failed' && result.recoverable === true &&
    (typeof result.error_code === 'string' && errorCodes.has(result.error_code))
}

export async function changeTwoFactor(rawInput, signal, adapters = {}) {
  const input = validateChangeRequest(rawInput)
  if (signal?.aborted) return failure('primary', false, 'change_timeout')
  if (typeof adapters.primary !== 'function') return failure('primary', false, 'automation_unavailable')
  let primary
  try {
    primary = await adapters.primary({ email: input.email, password: input.password, oldTotpSecret: input.old_totp_secret }, signal)
  } catch {
    primary = { status: 'failed', error_code: 'automation_unavailable', recoverable: false }
  }
  if (signal?.aborted) return failure('primary', false, 'change_timeout')
  const primarySecret = adapterSecret(primary)
  if (primarySecret) return { status: 'success', stage: 'primary', fallback_used: false, new_totp_secret: primarySecret }
  const primaryFailure = primary?.status === 'success' ? 'replacement_unverified' : primary?.error_code
  if (!isRecoverable(primary)) return failure('primary', false, primaryFailure)
  if (typeof adapters.fallback !== 'function') return failure('primary', false, primaryFailure)
  let fallback
  try {
    // Moyanovo's force-email-skip switch intentionally remains disabled.
    fallback = await adapters.fallback({ email: input.email, password: input.password, oldTotpSecret: input.old_totp_secret, forceEmailSkip: false }, signal)
  } catch {
    fallback = { status: 'failed', error_code: 'automation_unavailable' }
  }
  if (signal?.aborted) return failure('fallback', true, 'change_timeout')
  const fallbackSecret = adapterSecret(fallback)
  if (fallbackSecret) return { status: 'success', stage: 'fallback', fallback_used: true, new_totp_secret: fallbackSecret }
  return failure('fallback', true, fallback?.status === 'success' ? 'replacement_unverified' : fallback?.error_code)
}
