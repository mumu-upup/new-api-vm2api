import { describe, expect, it } from 'vitest'

import {
  MAX_CREDENTIAL_ROWS,
  parseCredentials,
  statusCheckFormSchema,
} from '../lib/parser'

describe('parseCredentials', () => {
  it('parses valid rows and normalizes the TOTP secret', () => {
    expect(
      parseCredentials(
        'owner@example.com----secret----jbswy3dpehpK3Pxp\nadmin@example.com----pass----JBSW Y3DP EHPK 3PXP'
      )
    ).toEqual([
      {
        email: 'owner@example.com',
        password: 'secret',
        totp_secret: 'JBSWY3DPEHPK3PXP',
      },
      {
        email: 'admin@example.com',
        password: 'pass',
        totp_secret: 'JBSWY3DPEHPK3PXP',
      },
    ])
  })

  it.each([
    ['malformed rows', 'owner@example.com----secret'],
    [
      'duplicate emails',
      'owner@example.com----a----JBSWY3DPEHPK3PXP\nOWNER@example.com----b----JBSWY3DPEHPK3PXP',
    ],
    ['empty password', 'owner@example.com------JBSWY3DPEHPK3PXP'],
    ['invalid TOTP', 'owner@example.com----secret----not-a-secret'],
  ])('rejects %s', (_label, input) => {
    expect(() => parseCredentials(input)).toThrow()
  })

  it('rejects more than the server limit', () => {
    const rows = Array.from(
      { length: MAX_CREDENTIAL_ROWS + 1 },
      (_, index) => `user${index}@example.com----secret----JBSWY3DPEHPK3PXP`
    ).join('\n')
    expect(() => parseCredentials(rows)).toThrow()
  })
})

it('preserves password whitespace and separators exactly', () => {
  expect(
    parseCredentials('owner@example.com---- p----word ----JBSWY3DPEHPK3PXP')[0]
      .password
  ).toBe(' p----word ')
})
it('rejects noncanonical Base32 and byte-length overflow without echoing inputs', () => {
  expect(() =>
    parseCredentials('owner@example.com----p----AAAAAAAAAAAAAAAAAB')
  ).toThrow('invalid_credentials')
  expect(() =>
    parseCredentials(
      `owner@example.com----${'中'.repeat(400)}----JBSWY3DPEHPK3PXP`
    )
  ).toThrow('invalid_credentials')
})

describe('model test form validation', () => {
  const values = {
    credentials: 'owner@example.com----secret----JBSWY3DPEHPK3PXP',
    mode: 'model',
    model: 'gpt-6.1-sol',
    prompt: 'What is 9 + 9? Reply with only the integer.',
    expected_answer: '18',
  }

  it.each([
    ['model', ''],
    ['model', 'bad/model'],
    ['model', 'a'.repeat(101)],
    ['prompt', '   '],
    ['prompt', '中'.repeat(1334)],
    ['expected_answer', '中'.repeat(334)],
  ])('rejects invalid or oversized %s before submission', (field, value) => {
    const result = statusCheckFormSchema.safeParse({
      ...values,
      [field]: value,
    })
    expect(result.success).toBe(false)
    if (!result.success) expect(result.error.issues[0]?.path).toEqual([field])
  })

  it('accepts exact UTF-8 byte limits and an empty expected answer', () => {
    expect(
      statusCheckFormSchema.safeParse({
        ...values,
        model: 'a'.repeat(100),
        prompt: `${'中'.repeat(1333)}a`,
        expected_answer: '',
      }).success
    ).toBe(true)
    expect(
      statusCheckFormSchema.safeParse({
        ...values,
        expected_answer: `${'中'.repeat(333)}a`,
      }).success
    ).toBe(true)
  })

  it('ignores model fields when login-only mode is selected', () => {
    expect(
      statusCheckFormSchema.safeParse({
        ...values,
        mode: 'login',
        model: '',
        prompt: '',
        expected_answer: 'x'.repeat(1001),
      }).success
    ).toBe(true)
  })
})
