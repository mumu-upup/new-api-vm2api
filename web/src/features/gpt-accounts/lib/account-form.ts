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
  auto_relogin: z.boolean(),
  group: z.string().trim().min(1, 'Group is required'),
  models: z.string().trim(),
  channel_id: z.number().int().min(0),
})

export type AccountFormValues = z.infer<typeof accountFormSchema>
