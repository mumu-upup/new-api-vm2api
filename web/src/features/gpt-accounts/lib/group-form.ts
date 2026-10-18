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
  default_models: z
    .string()
    .trim()
    .refine((value) => {
      if (
        !value ||
        new TextEncoder().encode(value).length > 8192 ||
        ['\r', '\n', '\0'].some((character) => value.includes(character))
      ) {
        return false
      }
      return value.split(',').every((name) => {
        const version =
          /^gpt-(\d+)(?:\.(\d+))?(?:-[a-z0-9][a-z0-9.-]*)?$/i.exec(name.trim())
        if (!version) return false
        const major = Number(version[1])
        return major > 5 || (major === 5 && Number(version[2] ?? 0) >= 6)
      })
    }, 'Enter GPT model names at version 5.6 or later.'),
  default_gateway_id: z.string().optional(),
})
