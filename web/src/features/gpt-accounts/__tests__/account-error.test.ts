import type { TFunction } from 'i18next'
import { expect, it } from 'vitest'

import { getAccountErrorMessage } from '../lib/account-error'

const translate = ((key: string) => key) as TFunction

it.each([
  [
    'gateway_import_uncertain',
    'Gateway import could not be confirmed. Synchronize the gateway before signing in again.',
  ],
  ['gateway_unavailable', 'Gateway synchronization failed'],
  ['gateway_channel_json_invalid', 'The channel JSON is invalid.'],
  [
    'gateway_channel_provider_required',
    'Choose CPA, Sub2API, or Prism in the channel JSON.',
  ],
  ['gateway_channel_url_required', 'Provide a relay URL in the channel JSON.'],
  ['gateway_channel_url_invalid', 'The relay URL is invalid or unsafe.'],
  [
    'gateway_channel_key_required',
    'Provide a relay API key in the channel JSON.',
  ],
  ['gateway_channel_key_invalid', 'The relay API key is invalid.'],
  ['gateway_channel_models_invalid', 'The channel model list is invalid.'],
  ['gateway_channel_create_failed', 'The relay channel could not be created.'],
  [
    'session_cleanup_unverified',
    'Sign-in succeeded, but other ChatGPT sessions could not be safely signed out.',
  ],
])('maps %s to a specific channel import message', (code, message) => {
  expect(getAccountErrorMessage(code, translate)).toBe(message)
})

it.each([
  [
    'instance_unavailable',
    'The instance manager is unavailable. Try again later.',
  ],
  [
    'instance_invalid_response',
    'The instance manager returned an invalid response. Retry synchronization.',
  ],
  [
    'instance_capacity_exhausted',
    'Instance capacity is full. Contact the administrator or try again later.',
  ],
  [
    'instance_not_found',
    'The instance was not found. Retry synchronization to restore it.',
  ],
  [
    'instance_not_running',
    'The instance is stopped. Start it before retrying.',
  ],
  [
    'instance_not_ready',
    'The instance is not ready. Wait for synchronization and try again.',
  ],
  [
    'instance_migration_failed',
    'Account migration failed. Retry synchronization.',
  ],
  [
    'instance_source_not_paused',
    'The shared slot could not be paused. Retry migration before using the dedicated instance.',
  ],
])('maps %s to a safe actionable instance message', (code, message) => {
  expect(getAccountErrorMessage(code, translate)).toBe(message)
})
