import { z } from 'zod'

export const accountGroupSchema = z
  .string()
  .trim()
  .refine(
    (value) =>
      new TextEncoder().encode(value).length <= 64 &&
      !['\r', '\n', '\0'].some((character) => value.includes(character)) &&
      (!value || value.split(',').every((name) => name.trim())),
    'Enter a valid group of up to 64 bytes.'
  )

export const defaultGroupSchema = z.object({
  default_group: accountGroupSchema.refine(Boolean, 'Group is required'),
})
