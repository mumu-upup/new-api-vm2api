import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { GptAccountStatusCheck } from '..'
import type { StatusCheckView } from '../api'

const credentials = 'owner@example.com----private-password----JBSWY3DPEHPK3PXP'
const completed: StatusCheckView = {
  job_id: 'job-1',
  state: 'completed',
  total: 1,
  completed: 1,
  success: 1,
  failed: 0,
  needs_attention: 0,
  cancelled: 0,
  results: [{ email: 'o***@example.com', status: 'success' }],
}
const running: StatusCheckView = {
  ...completed,
  state: 'running',
  completed: 0,
  success: 0,
  results: [{ email: 'o***@example.com', status: 'pending' }],
}
let client: QueryClient

beforeEach(() => {
  sessionStorage.clear()
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: completed },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: completed },
  })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})
function renderPage() {
  return render(
    <QueryClientProvider client={client}>
      <GptAccountStatusCheck />
    </QueryClientProvider>
  )
}
function enterCredentials() {
  fireEvent.change(screen.getByLabelText('Credentials'), {
    target: { value: credentials },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Check status' }))
}

it('keeps empty input disabled and exposes the result placeholder', () => {
  renderPage()
  expect(screen.getByRole('button', { name: 'Check status' })).toBeDisabled()
  expect(screen.getByText('No checks yet')).toBeVisible()
})
it('clears credentials before awaiting creation and prevents duplicate submissions', async () => {
  let resolve!: (value: unknown) => void
  vi.mocked(api.post).mockImplementationOnce(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  renderPage()
  enterCredentials()
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
  expect(screen.getByLabelText('Credentials')).toHaveValue('')
  expect(screen.getByLabelText('Requested model')).toBeDisabled()
  expect(screen.getByLabelText('Test question')).toBeDisabled()
  expect(screen.getByLabelText('Expected answer (optional)')).toBeDisabled()
  expect(screen.getByRole('radio', { name: 'Sign-in only' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.getByRole('button', { name: 'Check status' })).toBeDisabled()
  expect(
    JSON.stringify(
      client
        .getMutationCache()
        .getAll()
        .map((m) => m.state)
    )
  ).not.toContain('private-password')
  await act(async () => {
    resolve({ data: { success: true, data: completed } })
  })
  expect(await screen.findByText('o***@example.com')).toBeVisible()
  expect(screen.queryByText('private-password')).not.toBeInTheDocument()
})
it('reports invalid rows without sending credentials and permits correction', async () => {
  renderPage()
  fireEvent.change(screen.getByLabelText('Credentials'), {
    target: { value: 'invalid-row' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Check status' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Check the account format, remove duplicates, and use at most 100 rows.'
  )
  expect(api.post).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Credentials')).toHaveValue('invalid-row')
})
it('shows a safe error and clears input after creation fails', async () => {
  vi.mocked(api.post).mockRejectedValueOnce(new Error('request failed'))
  renderPage()
  enterCredentials()
  await waitFor(() =>
    expect(screen.getByLabelText('Credentials')).toHaveValue('')
  )
  expect(await screen.findByRole('alert')).toBeVisible()
})
it('stops an active check and replaces the polled running result', async () => {
  const cancelled: StatusCheckView = {
    ...running,
    state: 'cancelled',
    completed: 1,
    cancelled: 1,
    results: [{ email: 'o***@example.com', status: 'cancelled' }],
  }
  vi.mocked(api.post)
    .mockResolvedValueOnce({ data: { success: true, data: running } })
    .mockResolvedValueOnce({ data: { success: true, data: cancelled } })
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: running },
  })
  renderPage()
  enterCredentials()
  const stop = await screen.findByRole('button', { name: 'Cancel check' })
  await waitFor(() => expect(api.get).toHaveBeenCalled())
  fireEvent.click(stop)
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Cancel check' })
    ).not.toBeInTheDocument()
  )
  expect(screen.getByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '100'
  )
})
it('shows polling errors with a retry action instead of silently leaving stale progress', async () => {
  vi.mocked(api.post).mockResolvedValueOnce({
    data: { success: true, data: running },
  })
  vi.mocked(api.get).mockRejectedValue(new Error('unavailable'))
  renderPage()
  enterCredentials()
  expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
})
it('drops the previous administrator result when the user changes', async () => {
  renderPage()
  enterCredentials()
  expect(await screen.findByText('o***@example.com')).toBeVisible()
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 2, username: 'other', role: 10 })
  )
  expect(screen.queryByText('o***@example.com')).not.toBeInTheDocument()
})
it('denies non-admin users without making requests', () => {
  useAuthStore.getState().auth.setUser({ id: 3, username: 'member', role: 1 })
  renderPage()
  expect(screen.getByText('Access denied')).toBeVisible()
  expect(api.get).not.toHaveBeenCalled()
})

it('allows a new check when the previous job has expired', async () => {
  vi.mocked(api.post).mockResolvedValueOnce({
    data: { success: true, data: running },
  })
  vi.mocked(api.get).mockResolvedValue({
    data: { success: false, code: 'status_check_not_found' },
  })
  renderPage()
  enterCredentials()
  expect(
    await screen.findByText('This check has expired. Start a new check.')
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Retry' })
  ).not.toBeInTheDocument()
  expect(screen.getByLabelText('Credentials')).toBeEnabled()
  expect(
    screen.queryByRole('button', { name: 'Cancel check' })
  ).not.toBeInTheDocument()
})

it('recovers the running job after remount without storing credentials', async () => {
  vi.mocked(api.post).mockResolvedValueOnce({
    data: { success: true, data: running },
  })
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: running },
  })
  const mounted = renderPage()
  enterCredentials()
  expect(
    await screen.findByRole('button', { name: 'Cancel check' })
  ).toBeVisible()
  const stored = JSON.stringify(Object.entries(sessionStorage))
  expect(stored).toContain('job-1')
  expect(stored).not.toContain('private-password')
  expect(stored).not.toContain('JBSWY3DPEHPK3PXP')
  mounted.unmount()
  renderPage()
  expect(
    await screen.findByRole('button', { name: 'Cancel check' })
  ).toBeVisible()
  expect(screen.getByLabelText('Credentials')).toHaveValue('')
})

it('defaults to a real model test and submits the edited question without persisting it', async () => {
  renderPage()
  expect(
    screen.getByRole('radio', { name: 'Sign-in and model answer' })
  ).toBeChecked()
  expect(screen.getByLabelText('Requested model')).toHaveValue('gpt-6.1-sol')
  fireEvent.change(screen.getByLabelText('Test question'), {
    target: { value: 'Reply with exactly 18.' },
  })
  fireEvent.change(screen.getByLabelText('Expected answer (optional)'), {
    target: { value: '18' },
  })
  enterCredentials()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-account-status-checks',
      {
        credentials,
        model_test: {
          model: 'gpt-6.1-sol',
          prompt: 'Reply with exactly 18.',
          expected_answer: '18',
        },
      },
      expect.objectContaining({
        singleUseAuthorization: true,
        skipErrorHandler: true,
      })
    )
  )
  expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain(
    'Reply with exactly 18.'
  )
})

it('switches to login-only mode with the keyboard and omits model options', async () => {
  const user = userEvent.setup()
  renderPage()
  screen.getByRole('radio', { name: 'Sign-in and model answer' }).focus()
  await user.keyboard('{ArrowLeft}')
  expect(screen.getByRole('radio', { name: 'Sign-in only' })).toBeChecked()
  expect(screen.queryByLabelText('Test question')).not.toBeInTheDocument()
  enterCredentials()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-account-status-checks',
      { credentials },
      expect.anything()
    )
  )
})

it('shows a model validation error without sending or clearing credentials', async () => {
  renderPage()
  fireEvent.change(screen.getByLabelText('Requested model'), {
    target: { value: 'invalid/model' },
  })
  enterCredentials()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Use a model ID of 1–100 letters, digits, dots, underscores, colons, or hyphens, starting with a letter or digit.'
  )
  expect(screen.getByLabelText('Requested model')).toHaveAttribute(
    'aria-invalid',
    'true'
  )
  expect(screen.getByLabelText('Credentials')).toHaveValue(credentials)
  expect(api.post).not.toHaveBeenCalled()
})

it('keeps sign-in success visible when the model is unavailable', async () => {
  const view: StatusCheckView = {
    ...completed,
    success: 0,
    failed: 1,
    model_test: {
      model: 'gpt-6.1-sol',
      prompt: 'Reply 18.',
      expected_answer: '18',
    },
    results: [
      {
        email: 'o***@example.com',
        status: 'failed',
        login_status: 'success',
        duration_ms: 1234,
        model_test: {
          status: 'failed',
          error_code: 'model_not_available',
          model: 'gpt-6.1-sol',
          elapsed_ms: 200,
          evaluation: 'unscored',
        },
      },
    ],
  }
  vi.mocked(api.post).mockResolvedValue({ data: { success: true, data: view } })
  vi.mocked(api.get).mockResolvedValue({ data: { success: true, data: view } })
  renderPage()
  enterCredentials()
  const row = await screen.findByRole('row', { name: /o\*\*\*@example.com/ })
  expect(within(row).getByText('Sign-in: Success')).toBeVisible()
  expect(
    within(row).getByText(
      'This model is not available for this account. Try another model ID.'
    )
  ).toBeVisible()
  expect(within(row).getByText('1,234 ms')).toBeVisible()
})

it('shows returned text and answer mismatch separately from successful model access after reload', async () => {
  sessionStorage.setItem('gpt-account-status-check:1:', 'job-1')
  const view: StatusCheckView = {
    ...completed,
    model_test: {
      model: 'gpt-6.1-sol',
      prompt: 'Reply with the integer 18.',
      expected_answer: '18',
    },
    results: [
      {
        email: 'o***@example.com',
        status: 'success',
        login_status: 'success',
        duration_ms: 5678,
        model_test: {
          status: 'success',
          model: 'gpt-6.1-sol',
          actual_model: 'gpt-6.1-sol-2026-10-01',
          answer: '<script>19</script>',
          elapsed_ms: 2500,
          evaluation: 'mismatch',
        },
      },
    ],
  }
  vi.mocked(api.get).mockResolvedValue({ data: { success: true, data: view } })
  renderPage()
  const table = await screen.findByRole('table', {
    name: 'Status check results',
  })
  expect(within(table).getByText('Answer differs')).toBeVisible()
  expect(within(table).getByText('gpt-6.1-sol-2026-10-01')).toBeVisible()
  expect(within(table).getByText('<script>19</script>')).toBeVisible()
  expect(
    within(table).getByText('<script>19</script>').querySelector('script')
  ).toBeNull()
  expect(within(table).getByLabelText('Model answer')).toHaveClass(
    'max-h-64',
    'overflow-auto',
    'whitespace-pre-wrap'
  )
  within(table).getByLabelText('Model answer').focus()
  expect(within(table).getByLabelText('Model answer')).toHaveFocus()
  expect(within(table).getByText('2,500 ms')).toBeVisible()
  expect(screen.getByText('Reply with the integer 18.')).toBeVisible()
  expect(
    screen.getAllByText(
      'Matching is case-sensitive after trimming whitespace and normalizing line endings. A single answer cannot establish hidden model downgrading.'
    ).length
  ).toBeGreaterThan(0)
  expect(screen.getByText('Success: 1')).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
})

it.each([
  [
    'model_access_denied',
    'This account does not have access to the requested model.',
  ],
  [
    'model_rate_limited',
    'The model request was rate limited. Try again later.',
  ],
  ['model_quota_exhausted', 'This account has exhausted its model quota.'],
  [
    'model_authentication_failed',
    'The model service rejected the temporary authorization. Start a new check.',
  ],
  ['model_timeout', 'The model request timed out.'],
  ['model_network_error', 'The model service could not be reached.'],
  [
    'model_upstream_error',
    'The model service returned an error. Try again later.',
  ],
  ['model_response_invalid', 'The model service returned an invalid response.'],
  ['model_response_incomplete', 'The model response did not complete.'],
  [
    'model_output_too_large',
    'The model response exceeded the check size limit.',
  ],
  ['model_no_answer', 'The model completed without a text answer.'],
  [
    'identity_verification_failed',
    'Account identity verification failed. Start a new check.',
  ],
  [
    'authorization_failed',
    'Temporary account authorization failed. Start a new check.',
  ],
  [
    'raw-private-upstream-error',
    'Sign-in needs attention. Retry or complete browser sign-in.',
  ],
])('renders a safe diagnosis for %s', async (code, message) => {
  sessionStorage.setItem('gpt-account-status-check:1:', 'job-1')
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: {
        ...completed,
        success: 0,
        failed: 1,
        results: [
          { email: 'o***@example.com', status: 'failed', error_code: code },
        ],
      },
    },
  })
  renderPage()
  expect(await screen.findByText(message)).toBeVisible()
  expect(screen.queryByText(code)).not.toBeInTheDocument()
})

it.each([
  ['matched', 'Answer matched'],
  ['unscored', 'Not scored'],
])(
  'shows %s scoring without changing successful model access',
  async (evaluation, label) => {
    sessionStorage.setItem('gpt-account-status-check:1:', 'job-1')
    vi.mocked(api.get).mockResolvedValue({
      data: {
        success: true,
        data: {
          ...completed,
          model_test: {
            model: 'gpt-6.1-sol',
            prompt: 'Reply 18.',
            expected_answer: '',
          },
          results: [
            {
              email: 'o***@example.com',
              status: 'success',
              login_status: 'success',
              model_test: {
                status: 'success',
                model: 'gpt-6.1-sol',
                answer: '18',
                elapsed_ms: 12,
                evaluation,
              },
            },
          ],
        },
      },
    })
    renderPage()
    const table = await screen.findByRole('table', {
      name: 'Status check results',
    })
    expect(within(table).getByText(label)).toBeVisible()
    expect(within(table).getByText('Model responded')).toBeVisible()
  }
)
