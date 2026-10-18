import { describe, expect, it } from 'vitest'

import { gatewayFormSchema } from '../lib/gateway-form'

const vm2apiValues = {
  name: 'VM2API pool',
  provider: 'vm2api' as const,
  management_version: '' as const,
  management_url: 'https://api.modelvivid.com/vm2api',
  relay_url: 'https://api.modelvivid.com/vm2api',
  allow_local_http: false,
  remote_group_id: 0,
  group: '',
  models: 'gpt-5',
  management_key: 'master-key',
  relay_key: 'relay-key',
}

describe('gatewayFormSchema', () => {
  it('accepts a new vm2api gateway without a management version or remote group', () => {
    const result = gatewayFormSchema(false).safeParse(vm2apiValues)

    expect(result.success).toBe(true)
  })

  it('rejects vm2api when a management version is submitted', () => {
    const result = gatewayFormSchema(false).safeParse({
      ...vm2apiValues,
      management_version: 'v8',
    })

    expect(result.success).toBe(false)
  })

  it('rejects vm2api when management and relay URLs differ', () => {
    const result = gatewayFormSchema(false).safeParse({
      ...vm2apiValues,
      relay_url: 'https://relay.modelvivid.com/vm2api',
    })

    expect(result.success).toBe(false)
  })
})
