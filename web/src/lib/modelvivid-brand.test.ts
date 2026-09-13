import { describe, expect, it } from 'vitest'

import { DEFAULT_SYSTEM_NAME } from './constants'
import { MODELVIVID_BRAND } from './modelvivid-brand'

describe('ModelVivid public branding', () => {
  it('uses the public brand and canonical integration guide path', () => {
    expect(DEFAULT_SYSTEM_NAME).toBe('ModelVivid')
    expect(MODELVIVID_BRAND.name).toBe('ModelVivid')
    expect(MODELVIVID_BRAND.docsPath).toBe('/docs/codex')
  })
})
