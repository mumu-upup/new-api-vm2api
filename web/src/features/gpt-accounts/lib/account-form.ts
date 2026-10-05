import { z } from 'zod'

export const accountFormSchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(128),
  email: z.email('Please enter a valid email address').trim().toLowerCase(),
  password: z
    .string()
    .refine(
      (value) => new TextEncoder().encode(value).length <= 1024,
      'Account password is too long'
    ),
  totp_secret: z
    .string()
    .trim()
    .transform((value) => value.replaceAll(' ', '').toUpperCase())
    .refine((value) => {
      if (!value) return true
      if (!/^[A-Z2-7]{16,128}$/.test(value)) return false
      const spareBits = (value.length * 5) % 8
      if (spareBits >= 5) return false
      const lastDigit = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(
        value.at(-1) ?? ''
      )
      return (lastDigit & ((1 << spareBits) - 1)) === 0
    }, 'Enter a Base32 TOTP secret'),
  totp_provider: z.enum(['local', '2fa_cn']),
  auto_relogin: z.boolean(),
  group: z.string().trim().min(1, 'Group is required'),
  models: z.string().trim(),
  channel_id: z.number().int().min(0),
  gateway_id: z.number().int().min(0),
})

export type AccountFormValues = z.infer<typeof accountFormSchema>

export const accountImportSchema = z.string().transform((value, context) => {
  const first = value.indexOf('----')
  const last = value.lastIndexOf('----')
  const credentials = accountFormSchema
    .pick({ email: true, password: true, totp_secret: true })
    .safeParse({
      email: value.slice(0, first).trim(),
      password: value.slice(first + 4, last),
      totp_secret: value.slice(last + 4),
    })
  if (
    /[\r\n]/.test(value) ||
    first <= 0 ||
    last <= first + 4 ||
    !credentials.success ||
    !credentials.data.password ||
    !credentials.data.totp_secret
  ) {
    context.addIssue({
      code: 'custom',
      message: 'Enter one account as email----password----TOTP secret.',
    })
    return z.NEVER
  }
  return credentials.data
})

export const accountTotpSchema = z.object({
  totp_secret: accountFormSchema.shape.totp_secret.refine(
    (value) => value.length > 0,
    'Enter a Base32 TOTP secret'
  ),
  confirmed_bound: z
    .boolean()
    .refine(
      (value) => value,
      'Confirm that official authenticator binding is complete.'
    ),
})

export type AccountTotpValues = z.infer<typeof accountTotpSchema>
