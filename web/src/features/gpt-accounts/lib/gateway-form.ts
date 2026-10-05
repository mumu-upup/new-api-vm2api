import { z } from 'zod'

export function gatewayFormSchema(editing: boolean) {
  return z
    .object({
      name: z.string().trim().min(1, 'Name is required').max(128),
      provider: z.enum(['cpa', 'sub2api']),
      management_version: z.enum(['', 'v8', 'legacy']),
      management_url: z
        .string()
        .trim()
        .min(1, 'Enter a valid gateway address.'),
      relay_url: z.string().trim().min(1, 'Enter a valid gateway address.'),
      allow_local_http: z.boolean(),
      remote_group_id: z.number().int().min(0),
      group: z.string().trim().min(1, 'Group is required').max(64),
      models: z.string().trim().max(8192),
      management_key: z.string().max(8192),
      relay_key: z.string().max(8192),
    })
    .superRefine((values, context) => {
      for (const field of ['management_url', 'relay_url'] as const) {
        try {
          const url = new URL(values[field])
          const local = ['localhost', '127.0.0.1', '[::1]'].includes(
            url.hostname
          )
          if (
            url.username ||
            url.password ||
            url.search ||
            url.hash ||
            (url.protocol !== 'https:' &&
              !(url.protocol === 'http:' && local && values.allow_local_http))
          ) {
            throw new Error('invalid address')
          }
        } catch {
          context.addIssue({
            code: 'custom',
            path: [field],
            message:
              'Use an HTTPS address, or enable local HTTP for a loopback address.',
          })
        }
      }
      if (values.provider === 'sub2api' && values.remote_group_id <= 0) {
        context.addIssue({
          code: 'custom',
          path: ['remote_group_id'],
          message: 'Choose a Sub2API group ID greater than zero.',
        })
      }
      if (!editing) {
        if (!values.management_key.trim()) {
          context.addIssue({
            code: 'custom',
            path: ['management_key'],
            message: 'Management key is required',
          })
        }
        if (!values.relay_key.trim()) {
          context.addIssue({
            code: 'custom',
            path: ['relay_key'],
            message: 'Relay key is required',
          })
        }
      }
    })
}

export type GatewayFormValues = z.infer<ReturnType<typeof gatewayFormSchema>>
