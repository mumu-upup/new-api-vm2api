import type { TFunction } from 'i18next'

import { getAccountErrorMessage } from '@/features/gpt-accounts/lib/account-error'

export function getStatusCheckErrorMessage(code: string, t: TFunction): string {
  switch (code) {
    case 'automation_unavailable':
    case 'worker_unavailable':
      return t(
        'The account check worker is unavailable. Contact the administrator.'
      )
    case 'model_not_available':
      return t(
        'This model is not available for this account. Try another model ID.'
      )
    case 'model_access_denied':
      return t('This account does not have access to the requested model.')
    case 'model_rate_limited':
      return t('The model request was rate limited. Try again later.')
    case 'model_quota_exhausted':
      return t('This account has exhausted its model quota.')
    case 'model_authentication_failed':
      return t(
        'The model service rejected the temporary authorization. Start a new check.'
      )
    case 'model_timeout':
      return t('The model request timed out.')
    case 'model_network_error':
      return t('The model service could not be reached.')
    case 'model_upstream_error':
      return t('The model service returned an error. Try again later.')
    case 'model_response_invalid':
      return t('The model service returned an invalid response.')
    case 'model_response_incomplete':
      return t('The model response did not complete.')
    case 'model_output_too_large':
      return t('The model response exceeded the check size limit.')
    case 'model_no_answer':
      return t('The model completed without a text answer.')
    case 'identity_verification_failed':
      return t('Account identity verification failed. Start a new check.')
    case 'authorization_failed':
      return t('Temporary account authorization failed. Start a new check.')
    default:
      return getAccountErrorMessage(code, t)
  }
}
