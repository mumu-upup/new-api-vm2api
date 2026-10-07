import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

export type ModelTestOptions = {
  model: string
  prompt: string
  expected_answer: string
}

export type ModelTestResult = {
  status: 'success' | 'failed' | 'needs_attention' | 'skipped'
  error_code?: string
  model: string
  actual_model?: string
  answer?: string
  elapsed_ms: number
  evaluation: 'matched' | 'mismatch' | 'unscored'
}

export type StatusCheckResult = {
  email: string
  status: string
  error_code?: string
  login_status?: 'success' | 'failed' | 'needs_attention'
  duration_ms?: number
  model_test?: ModelTestResult
}

export type StatusCheckView = {
  job_id: string
  state: 'pending' | 'running' | 'completed' | 'cancelled'
  total: number
  completed: number
  success: number
  failed: number
  needs_attention: number
  cancelled: number
  results: StatusCheckResult[]
  model_test?: ModelTestOptions
}

export function createStatusCheck(
  credentials: string,
  signal?: AbortSignal,
  modelTest?: ModelTestOptions
) {
  return authResult<StatusCheckView>(
    api.post(
      '/api/gpt-account-status-checks',
      { credentials, ...(modelTest ? { model_test: modelTest } : {}) },
      {
        ...authRequestOptions,
        signal,
        disableDuplicate: true,
        singleUseAuthorization: true,
      }
    )
  )
}

export function getStatusCheck(jobId: string, signal?: AbortSignal) {
  return authResult<StatusCheckView>(
    api.get(`/api/gpt-account-status-checks/${encodeURIComponent(jobId)}`, {
      ...authRequestOptions,
      signal,
      disableDuplicate: true,
    })
  )
}

export function cancelStatusCheck(jobId: string, signal?: AbortSignal) {
  return authResult<StatusCheckView>(
    api.post(
      `/api/gpt-account-status-checks/${encodeURIComponent(jobId)}/cancel`,
      {},
      {
        ...authRequestOptions,
        signal,
        disableDuplicate: true,
        singleUseAuthorization: true,
      }
    )
  )
}
