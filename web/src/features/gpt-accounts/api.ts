import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

import type {
  GptAccount,
  GptAccountAuthorization,
  GptAccountConfig,
  GptAccountInput,
  GptAccountTotpReveal,
  GptAccountGateway,
  GptAccountGatewayInput,
  GptAccountVm2ApiLogs,
  GptAccountVm2ApiStatus,
} from './types'

export function listGptAccounts(
  page: number,
  pageSize: number,
  signal?: AbortSignal
) {
  return authResult<{ items: GptAccount[]; total: number }>(
    api.get('/api/gpt-accounts', {
      ...authRequestOptions,
      params: { p: page, page_size: pageSize },
      signal,
      disableDuplicate: true,
    })
  )
}

export function getGptAccountVm2ApiStatus(id: number, signal?: AbortSignal) {
  return authResult<GptAccountVm2ApiStatus>(
    api.get(`/api/gpt-accounts/${id}/vm2api`, { ...authRequestOptions, signal })
  )
}

export function getGptAccountVm2ApiLogs(id: number, signal?: AbortSignal) {
  return authResult<GptAccountVm2ApiLogs>(
    api.get(`/api/gpt-accounts/${id}/vm2api/logs`, {
      ...authRequestOptions,
      params: { limit: 25 },
      signal,
    })
  )
}

export function setGptAccountVm2ApiSchedulable(
  id: number,
  schedulable: boolean
) {
  return authResult<GptAccountVm2ApiStatus>(
    api.patch(
      `/api/gpt-accounts/${id}/vm2api/schedulable`,
      { schedulable },
      authRequestOptions
    )
  )
}

export function refreshGptAccountVm2ApiQuota(id: number) {
  return authResult<GptAccountVm2ApiStatus>(
    api.post(
      `/api/gpt-accounts/${id}/vm2api/quota/refresh`,
      {},
      authRequestOptions
    )
  )
}

export function getGptAccountConfig(signal?: AbortSignal) {
  return authResult<GptAccountConfig>(
    api.get('/api/gpt-accounts/config', { ...authRequestOptions, signal })
  )
}

export function updateGptAccountConfig(
  input: Partial<
    Pick<GptAccountConfig, 'default_group' | 'default_models' | 'default_gateway_id'>
  >
) {
  return authResult<GptAccountConfig>(
    api.put('/api/gpt-accounts/config', input, authRequestOptions)
  )
}

export function saveGptAccount(
  id: number | undefined,
  input: GptAccountInput,
  signal: AbortSignal
) {
  const options = {
    ...authRequestOptions,
    singleUseAuthorization: true,
    signal,
  }
  return authResult<GptAccount>(
    id
      ? api.put(`/api/gpt-accounts/${id}`, input, options)
      : api.post('/api/gpt-accounts', input, options)
  )
}

export function importGptAccountOAuth(
  accountId: number | undefined,
  input: {
    oauth_json: string
    name?: string
    group?: string
    models?: string
    auto_relogin?: boolean
    gateway_id?: number
  },
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(
      accountId
        ? `/api/gpt-accounts/${accountId}/import-oauth`
        : '/api/gpt-accounts/import-oauth',
      input,
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function runGptAccountAction(
  id: number,
  action: 'login' | 'refresh' | 'sync',
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(
      `/api/gpt-accounts/${id}/${action}`,
      {},
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function listGptAccountGateways(signal?: AbortSignal) {
  return authResult<{ items: GptAccountGateway[] }>(
    api.get('/api/gpt-account-gateways', { ...authRequestOptions, signal })
  )
}

export function saveGptAccountGateway(
  id: number | undefined,
  input: GptAccountGatewayInput,
  signal: AbortSignal
) {
  const options = {
    ...authRequestOptions,
    singleUseAuthorization: true,
    signal,
  }
  return authResult<GptAccountGateway>(
    id
      ? api.put(`/api/gpt-account-gateways/${id}`, input, options)
      : api.post('/api/gpt-account-gateways', input, options)
  )
}

export function importGptGatewayChannelJSON(
  configJson: string,
  signal: AbortSignal
) {
  return authResult<{ channel_id: number; name: string; provider: string }>(
    api.post(
      '/api/gpt-account-gateways/import-channel-json',
      { config_json: configJson },
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export async function deleteGptAccount(id: number, signal: AbortSignal) {
  const response = await api.delete(`/api/gpt-accounts/${id}`, {
    ...authRequestOptions,
    singleUseAuthorization: true,
    signal,
  })
  // Deletion may return no data; retain the common success/error contract.
  await authResult(
    Promise.resolve({
      data: { ...response.data, data: response.data.data ?? {} },
    })
  )
}

export function authorizeGptAccount(id: number, signal: AbortSignal) {
  return authResult<GptAccountAuthorization>(
    api.get(`/api/gpt-accounts/${id}/oauth`, {
      ...authRequestOptions,
      singleUseAuthorization: true,
      disableDuplicate: true,
      signal,
    })
  )
}

export function finishGptAccountAuthorization(
  id: number,
  redirectUrl: string,
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(
      `/api/gpt-accounts/${id}/callback`,
      { redirect_url: redirectUrl },
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function saveGptAccountTotp(
  id: number,
  input: { totp_secret: string; confirmed_bound: boolean },
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(`/api/gpt-accounts/${id}/totp`, input, {
      ...authRequestOptions,
      singleUseAuthorization: true,
      signal,
    })
  )
}

export function revealGptAccountTotp(id: number, signal: AbortSignal) {
  return authResult<GptAccountTotpReveal>(
    api.post(
      `/api/gpt-accounts/${id}/totp/reveal`,
      {},
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}
