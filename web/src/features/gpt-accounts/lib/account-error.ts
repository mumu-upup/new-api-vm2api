import type { TFunction } from 'i18next'

export function getAccountErrorMessage(
  code: string,
  t: TFunction,
  fallback?: string
): string {
  switch (code) {
    case 'encryption_unavailable':
      return t(
        'Credential encryption is unavailable. Contact the administrator.'
      )
    case 'automation_unavailable':
    case 'worker_unavailable':
      return t(
        'Automatic sign-in is unavailable. Use browser sign-in to authorize an account.'
      )
    case 'credentials_required':
      return t('Add an account password to enable automatic sign-in.')
    case 'account_busy':
      return t('Account sign-in is already in progress.')
    case 'worker_busy':
      return t('Please try again later.')
    case 'account_changed':
      return t('The account changed. Refresh and try again.')
    case 'authorization_invalid':
      return t(
        'The authorization link is invalid or expired. Generate a new link.'
      )
    case 'authorization_failed':
    case 'identity_verification_failed':
      return t('Authorization failed. Complete browser sign-in again.')
    case 'identity_mismatch':
      return t('The signed-in account does not match this email address.')
    case 'needs_attention':
    case 'captcha_required':
    case 'email_verification_required':
    case 'consent_required':
    case 'additional_verification_required':
      return t('Complete browser sign-in to continue.')
    case 'refresh_failed':
      return t('Credential refresh failed. Sign in again.')
    case 'channel_required':
      return t('Authorize this account to create its channel first.')
    case 'authorization_forbidden':
      return t('You do not have permission to manage this account.')
    case 'invalid_totp_secret':
      return t('Enter a Base32 TOTP secret')
    case 'invalid_channel':
      return t('Choose a Codex channel with a single credential.')
    case 'account_not_found':
      return t('This account no longer exists.')
    case 'invalid_account':
    case 'invalid_credentials':
      return t('Check the account details and try again.')
    case 'account_conflict':
      return t('An account already uses this email address or channel.')
    case 'account_disabled':
    case 'account_unavailable':
      return t('The upstream account is unavailable.')
    case 'login_timeout':
      return t('Sign-in timed out. Try again.')
    case 'operation_failed':
      return t('The operation failed. Try again.')
    default:
      return fallback
        ? t(fallback)
        : t('Sign-in needs attention. Retry or complete browser sign-in.')
  }
}
