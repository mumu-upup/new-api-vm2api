import { describe, expect, it } from 'vitest'

import { DOC_SECTIONS, buildCodeExamples } from './integration-guide'

describe('ModelVivid integration guide', () => {
  it('contains the complete Codex onboarding sections and canonical API URL', () => {
    expect(DOC_SECTIONS.map((section) => section.id)).toEqual([
      'quick-start',
      'models',
      'api-key',
      'base-url',
      'codex-cli',
      'openai-compatible',
      'claude-code',
      'first-request',
      'troubleshooting',
    ])
    const examples = buildCodeExamples('gpt-6-astra')
    expect(examples.baseUrl).toBe('https://api.modelvivid.com/v1')
    expect(examples.curl).toContain('Bearer YOUR_MODELVIVID_API_KEY')
    expect(examples.curl).toContain('gpt-6-astra')
  })
})
