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

export function getGptAccountConfig(signal?: AbortSignal) {
  return authResult<GptAccountConfig>(
    api.get('/api/gpt-accounts/config', { ...authRequestOptions, signal })
  )
}

export function saveGptAccount(
  id: number | undefined,
  input: GptAccountInput,
  proof: string,
  signal: AbortSignal
) {
  const options = {
    ...authRequestOptions,
    headers: { 'X-Security-Proof': proof },
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
  input: {
    oauth_json: string
    name?: string
    group?: string
    models?: string
    auto_relogin?: boolean
  },
  proof: string,
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post('/api/gpt-accounts/import-oauth', input, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proof },
      singleUseAuthorization: true,
      signal,
    })
  )
}

export function runGptAccountAction(
  id: number,
  action: 'login' | 'refresh' | 'sync',
  proof: string,
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(
      `/api/gpt-accounts/${id}/${action}`,
      {},
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proof },
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
  proof: string,
  signal: AbortSignal
) {
  const options = {
    ...authRequestOptions,
    headers: { 'X-Security-Proof': proof },
    singleUseAuthorization: true,
    signal,
  }
  return authResult<GptAccountGateway>(
    id
      ? api.put(`/api/gpt-account-gateways/${id}`, input, options)
      : api.post('/api/gpt-account-gateways', input, options)
  )
}

export async function deleteGptAccount(
  id: number,
  proof: string,
  signal: AbortSignal
) {
  const response = await api.delete(`/api/gpt-accounts/${id}`, {
    ...authRequestOptions,
    headers: { 'X-Security-Proof': proof },
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

export function authorizeGptAccount(
  id: number,
  proof: string,
  signal: AbortSignal
) {
  return authResult<GptAccountAuthorization>(
    api.get(`/api/gpt-accounts/${id}/oauth`, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proof },
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
  proof: string,
  signal: AbortSignal
) {
  return authResult<GptAccount>(
    api.post(`/api/gpt-accounts/${id}/totp`, input, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proof },
      singleUseAuthorization: true,
      signal,
    })
  )
}

export function revealGptAccountTotp(
  id: number,
  proof: string,
  signal: AbortSignal
) {
  return authResult<GptAccountTotpReveal>(
    api.post(
      `/api/gpt-accounts/${id}/totp/reveal`,
      {},
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proof },
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}
