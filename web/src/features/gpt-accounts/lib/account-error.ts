import type { TFunction } from 'i18next'

export function getAccountErrorMessage(
  code: string,
  t: TFunction,
  fallback?: string
): string {
  switch (code) {
    case 'protocol_immutable':
      return t('The account protocol cannot be changed after creation.')
    case 'instance_unavailable':
      return t('The instance manager is unavailable. Try again later.')
    case 'instance_invalid_response':
      return t(
        'The instance manager returned an invalid response. Retry synchronization.'
      )
    case 'instance_capacity_exhausted':
      return t(
        'Instance capacity is full. Contact the administrator or try again later.'
      )
    case 'instance_not_found':
      return t(
        'The instance was not found. Retry synchronization to restore it.'
      )
    case 'instance_not_running':
      return t('The instance is stopped. Start it before retrying.')
    case 'instance_not_ready':
      return t(
        'The instance is not ready. Wait for synchronization and try again.'
      )
    case 'instance_migration_failed':
      return t('Account migration failed. Retry synchronization.')
    case 'instance_source_not_paused':
      return t(
        'The shared slot could not be paused. Retry migration before using the dedicated instance.'
      )

    case 'gateway_import_uncertain':
      return t(
        'Gateway import could not be confirmed. Synchronize the gateway before signing in again.'
      )
    case 'gateway_unavailable':
    case 'gateway_import_failed':
      return t('Gateway synchronization failed')
    case 'job_active':
      return t(
        'An account check is already running. Cancel it or wait for it to finish.'
      )
    case 'job_limit':
      return t('The account check queue is full. Try again later.')
    case 'totp_not_challenged':
      return t(
        'Sign-in succeeded without a 2FA challenge. The TOTP secret could not be verified.'
      )
    case 'password_not_challenged':
      return t(
        'The sign-in page did not request a password. The credentials could not be verified.'
      )
    case 'encryption_unavailable':
      return t(
        'Credential encryption is unavailable. Contact the administrator.'
      )
    case 'automation_unavailable':
    case 'worker_unavailable':
    case 'security_flow_unavailable':
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
      return t('Complete browser sign-in to continue.')
    case 'session_cleanup_unverified':
      return t(
        'Sign-in succeeded, but other ChatGPT sessions could not be safely signed out.'
      )
    case 'workspace_selection_required':
      return t('Choose the account workspace through browser sign-in.')
    case 'captcha_required':
      return t(
        'The sign-in page requires a CAPTCHA. Complete it through browser sign-in.'
      )
    case 'email_verification_required':
      return t(
        'The sign-in page requires email verification. Complete it through browser sign-in.'
      )
    case 'consent_required':
      return t(
        'The sign-in page requires your consent. Complete it through browser sign-in.'
      )
    case 'additional_verification_required':
      return t(
        'The sign-in page requires additional verification. Complete it through browser sign-in.'
      )
    case 'login_navigation_blocked':
      return t('The sign-in page blocked navigation. Try browser sign-in.')
    case 'login_navigation_failed':
      return t(
        'Could not open the sign-in page. Try again or use browser sign-in.'
      )
    case 'email_submission_failed':
      return t('Could not submit the account email on the sign-in page.')
    case 'password_submission_failed':
      return t('Could not submit the account password on the sign-in page.')
    case 'totp_submission_failed':
      return t('Could not submit the authenticator code on the sign-in page.')
    case 'email_step_timeout':
      return t(
        'The sign-in page did not continue after the email step. Try again.'
      )
    case 'password_step_timeout':
      return t(
        'The sign-in page did not continue after the password step. Try again.'
      )
    case 'totp_step_timeout':
      return t(
        'The sign-in page did not continue after the authenticator code step. Try again.'
      )
    case 'login_screen_unrecognized':
      return t('The sign-in page could not be recognized. Try browser sign-in.')
    case 'totp_secret_required':
      return t('Add this account’s TOTP secret to generate sign-in codes.')
    case 'totp_provider_unavailable':
      return t('The sign-in code provider is unavailable. Try again.')
    case 'totp_provider_timeout':
      return t('The sign-in code provider timed out. Try again.')
    case 'totp_provider_invalid_response':
      return t('The sign-in code provider returned an invalid code. Try again.')
    case 'invalid_totp_code':
      return t(
        'The sign-in page rejected the authenticator code. Check the saved secret and try again.'
      )
    case 'refresh_failed':
      return t('Credential refresh failed. Sign in again.')
    case 'channel_required':
      return t('Authorize this account to create its channel first.')
    case 'channel_missing':
      return t(
        'The linked channel no longer exists. Edit this account to choose a channel.'
      )
    case 'authorization_forbidden':
      return t('You do not have permission to manage this account.')
    case 'totp_confirmation_required':
      return t('Confirm that official authenticator binding is complete.')
    case 'totp_secret_not_found':
      return t('No TOTP secret is stored for this account.')
    case 'invalid_totp_secret':
      return t('Enter a Base32 TOTP secret')
    case 'replacement_unverified':
    case 'replacement_invalid':
      return t('The operation failed. Try again.')
    case 'change_timeout':
      return t('Sign-in timed out. Try again.')
    case 'invalid_channel':
      return t('Choose a Codex channel with a single credential.')
    case 'gateway_channel_json_invalid':
      return t('The channel JSON is invalid.')
    case 'gateway_channel_provider_required':
      return t('Choose CPA, Sub2API, or Prism in the channel JSON.')
    case 'gateway_channel_url_required':
      return t('Provide a relay URL in the channel JSON.')
    case 'gateway_channel_url_invalid':
      return t('The relay URL is invalid or unsafe.')
    case 'gateway_channel_key_required':
      return t('Provide a relay API key in the channel JSON.')
    case 'gateway_channel_key_invalid':
      return t('The relay API key is invalid.')
    case 'gateway_channel_models_invalid':
      return t('The channel model list is invalid.')
    case 'gateway_channel_create_failed':
      return t('The relay channel could not be created.')
    case 'gateway_required':
      return t('Select a default VM2API gateway before adding accounts.')
    case 'account_not_found':
      return t('This account no longer exists.')
    case 'invalid_account':
    case 'invalid_credentials':
      return t('Check the account details and try again.')
    case 'oauth_import_invalid':
      return t('The OAuth JSON is invalid or expired.')
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
