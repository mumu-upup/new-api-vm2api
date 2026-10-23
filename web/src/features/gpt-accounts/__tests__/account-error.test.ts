import type { TFunction } from 'i18next'
import { expect, it } from 'vitest'

import { getAccountErrorMessage } from '../lib/account-error'

const translate = ((key: string) => key) as TFunction

it.each([
  [
    'protocol_immutable',
    'The account protocol cannot be changed after creation.',
  ],
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
