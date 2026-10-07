import { z } from 'zod'

import { accountImportSchema } from '@/features/gpt-accounts/lib/account-form'

export const MAX_CREDENTIAL_ROWS = 100

export type StatusCheckCredential = {
  email: string
  password: string
  totp_secret: string
}

export function parseCredentials(raw: string): StatusCheckCredential[] {
  const lines = raw.replaceAll('\r\n', '\n').split('\n')
  while (lines.length > 0 && lines.at(-1)?.trim() === '') lines.pop()
  if (lines.length === 0 || lines.length > MAX_CREDENTIAL_ROWS) {
    throw new Error('invalid_credentials')
  }

  const seen = new Set<string>()
  return lines.map((line) => {
    const result = accountImportSchema.safeParse(line)
    if (
      !result.success ||
      new TextEncoder().encode(result.data.email).length > 254
    ) {
      throw new Error('invalid_credentials')
    }
    const credential = result.data
    const key = credential.email.toLowerCase()
    if (seen.has(key)) throw new Error('invalid_credentials')
    seen.add(key)
    return {
      email: credential.email,
      password: credential.password,
      totp_secret: credential.totp_secret,
    }
  })
}

export const statusCheckFormSchema = z
  .object({
    credentials: z.string().superRefine((value, context) => {
      try {
        parseCredentials(value)
      } catch {
        context.addIssue({ code: 'custom', message: 'invalid_credentials' })
      }
    }),
    mode: z.enum(['login', 'model']),
    model: z.string(),
    prompt: z.string(),
    expected_answer: z.string(),
  })
  .superRefine((values, context) => {
    if (values.mode === 'login') return
    if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$/.test(values.model)) {
      context.addIssue({
        code: 'custom',
        path: ['model'],
        message: 'invalid_model',
      })
    }
    if (
      !values.prompt.trim() ||
      new TextEncoder().encode(values.prompt).length > 4000
    ) {
      context.addIssue({
        code: 'custom',
        path: ['prompt'],
        message: 'invalid_prompt',
      })
    }
    if (new TextEncoder().encode(values.expected_answer).length > 1000) {
      context.addIssue({
        code: 'custom',
        path: ['expected_answer'],
        message: 'invalid_expected_answer',
      })
    }
  })

export type StatusCheckFormValues = z.infer<typeof statusCheckFormSchema>
