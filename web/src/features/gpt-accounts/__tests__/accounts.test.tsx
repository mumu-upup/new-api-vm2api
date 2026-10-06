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
import { formatTimestamp } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { GptAccounts } from '..'
import type { GptAccount } from '../types'

const account: GptAccount = {
  id: 1,
  name: 'Work account',
  email: 'owner@example.com',
  channel_id: 12,
  channel_name: 'Codex',
  status: 'ready',
  last_error_code: '',
  last_login_at: 0,
  last_attempt_at: 0,
  next_retry_at: 0,
  created_at: 0,
  updated_at: 0,
  auto_relogin: true,
  has_password: true,
  has_totp: true,
  totp_version: 0,
  totp_updated_at: 0,
  totp_status: 'saved',
  can_manage: true,
  group: 'default',
  models: 'gpt-5.4',
}
const gateway = {
  id: 5,
  name: 'CPA pool',
  provider: 'cpa',
  management_version: 'v8',
  management_url: 'https://cpa.example.com',
  relay_url: 'https://relay.example.com',
  allow_local_http: false,
  remote_group_id: 0,
  group: 'default',
  models: 'gpt-5',
  channel_id: 23,
  channel_name: 'Shared CPA channel',
  has_management_key: true,
  has_relay_key: true,
}
let gateways: (typeof gateway)[]
let items: GptAccount[]
let client: QueryClient

beforeEach(() => {
  items = [{ ...account }]
  gateways = [{ ...gateway }]
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url === '/api/gpt-account-gateways') {
      return { data: { success: true, data: { items: gateways } } }
    }
    if (url === '/api/gpt-accounts') {
      return { data: { success: true, data: { items, total: items.length } } }
    }
    if (url === '/api/gpt-accounts/config') {
      return {
        data: {
          success: true,
          data: { encryption_available: true, automation_available: true },
        },
      }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: config?.params?.scope,
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'proof',
            scope: (body as { scope: string }).scope,
            method: 'password',
            expires_at: Date.now() / 1000 + 60,
          },
        },
      }
    }
    return {
      data: { success: true, data: { ...account, status: 'logging_in' } },
    }
  })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
  vi.useRealTimers()
})
function renderPage() {
  return render(
    <QueryClientProvider client={client}>
      <GptAccounts />
    </QueryClientProvider>
  )
}
async function verify() {
  await userEvent.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'admin-password'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

it('denies ordinary users without requesting managed account data', async () => {
  useAuthStore.getState().auth.setUser({ id: 2, username: 'user', role: 1 })
  renderPage()
  expect(screen.getByText('Access denied')).toBeVisible()
  expect(api.get).not.toHaveBeenCalled()
})

it('masks credential inputs and starts creation only after scoped admin verification', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Add GPT account' })
  await userEvent.click(
    within(dialog).getByRole('tab', { name: 'Enter separately' })
  )
  await userEvent.type(within(dialog).getByLabelText('Name'), 'Second account')
  await userEvent.type(
    within(dialog).getByLabelText('Email'),
    'second@example.com'
  )
  const password = within(dialog).getByLabelText('Account password')
  const totp = within(dialog).getByLabelText('TOTP secret')
  expect(password).toHaveAttribute('type', 'password')
  expect(totp).toHaveAttribute('type', 'password')
  await userEvent.type(password, 'upstream-password')
  await userEvent.type(totp, 'JBSWY3DPEHPK3PXP')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts',
    expect.anything(),
    expect.anything()
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts',
      expect.objectContaining({
        email: 'second@example.com',
        password: 'upstream-password',
        totp_secret: 'JBSWY3DPEHPK3PXP',
        totp_provider: 'local',
      }),
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'proof' },
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.create',
      context: { email: 'second@example.com' },
    }),
    expect.anything()
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Add GPT account' })
    ).not.toBeInTheDocument()
  )
})

it('imports Codex OAuth JSON only after email-scoped verification', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Import OAuth JSON' })
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Import Codex OAuth JSON',
  })
  const oauth =
    '{"access_token":"test-access","refresh_token":"test-refresh","id_token":"test-id","account_id":"test-account","email":"import@example.com","expired":"2099-01-02T03:04:05Z","type":"codex"}'
  fireEvent.change(within(dialog).getByLabelText('OAuth JSON'), {
    target: { value: oauth },
  })
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Import OAuth JSON' })
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/import-oauth',
      { oauth_json: oauth },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.oauth_import.create',
      context: { email: 'import@example.com' },
    }),
    expect.anything()
  )
  expect(
    screen.queryByRole('dialog', { name: 'Import Codex OAuth JSON' })
  ).not.toBeInTheDocument()
})

it('editing never prefills stored credentials and cancelling clears entered replacements', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  expect(screen.getByLabelText('Account password')).toHaveValue('')
  expect(screen.getByLabelText('TOTP secret')).toHaveValue('')
  await userEvent.type(screen.getByLabelText('Account password'), 'replacement')
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await userEvent.click(
    screen.getByRole('button', { name: 'Edit Work account' })
  )
  expect(screen.getByLabelText('Account password')).toHaveValue('')
})

it('shows upstream authentication failure as account status while preserving the admin session', async () => {
  items = [
    {
      ...account,
      status: 'needs_attention',
      last_error_code: 'upstream_unauthorized',
    },
  ]
  renderPage()
  expect(await screen.findByText('Needs attention')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Browser sign-in for Work account' })
  ).toBeEnabled()
  expect(useAuthStore.getState().auth.user?.id).toBe(1)
})

it('keeps a failed login action visible and does not report the account as ready', async () => {
  items = [{ ...account, status: 'error' }]
  vi.mocked(api.post).mockImplementation(async (url, body) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'proof',
            scope: (body as { scope: string }).scope,
            method: 'password',
            expires_at: Date.now() / 1000 + 60,
          },
        },
      }
    }
    return {
      data: {
        success: false,
        message: 'credentials_required',
        code: 'credentials_required',
      },
    }
  })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Sign in Work account' })
  )
  await verify()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Add an account password to enable automatic sign-in.'
  )
  expect(screen.queryByText('Ready')).not.toBeInTheDocument()
})

it('requires scoped verification for an official browser link and submits callback only to the server', async () => {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  if (!originalGet) throw new Error('GET fixture is unavailable')
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/gpt-accounts/1/oauth') {
      return {
        data: {
          success: true,
          data: {
            authorization_url:
              'https://auth.openai.com/oauth/authorize?state=expected',
            expires_at: Date.now() / 1000 + 600,
          },
        },
      }
    }
    return originalGet(url, config)
  })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', {
      name: 'Browser sign-in for Work account',
    })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Generate sign-in link' })
  )
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/oauth',
    expect.anything()
  )
  await verify()
  const browserDialog = await screen.findByRole('dialog', {
    name: 'Complete browser sign-in',
  })
  const link = await within(browserDialog).findByRole('link', {
    name: 'Open official sign-in',
  })
  expect(link).toHaveAttribute(
    'href',
    'https://auth.openai.com/oauth/authorize?state=expected'
  )
  expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  expect(api.get).toHaveBeenCalledWith(
    '/api/gpt-accounts/1/oauth',
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'proof' },
      singleUseAuthorization: true,
    })
  )
  const callback = screen.getByLabelText('Callback URL')
  expect(callback).toHaveAttribute('type', 'password')
  await userEvent.type(
    callback,
    'http://localhost:1455/auth/callback?code=one-use-code&state=expected'
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Complete authorization' })
  )
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/1/callback',
      {
        redirect_url:
          'http://localhost:1455/auth/callback?code=one-use-code&state=expected',
      },
      expect.objectContaining({ singleUseAuthorization: true })
    )
  )
})

it('keeps existing-channel linking out of an ordinary admin form', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  expect(
    screen.queryByLabelText('Existing Codex channel ID')
  ).not.toBeInTheDocument()
})

it('cancelling admin verification sends no account mutation', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Sign in Work account' })
  )
  await screen.findByLabelText('Password', { selector: 'input' })
  await userEvent.keyboard('{Escape}')
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/login',
    expect.anything(),
    expect.anything()
  )
})

it('updates processing status to ready after the server returns successful authentication', async () => {
  items = [{ ...account, status: 'logging_in' }]
  renderPage()
  expect(await screen.findByText('Signing in')).toBeVisible()
  items = [{ ...account, status: 'ready' }]
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['gpt-accounts'] })
  })
  expect(await screen.findByText('Ready')).toBeVisible()
})

it('clears submitted replacement credentials when verification is cancelled', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  await userEvent.type(screen.getByLabelText('Account password'), 'replacement')
  await userEvent.type(screen.getByLabelText('TOTP secret'), 'JBSWY3DPEHPK3PXP')
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  await screen.findByLabelText('Password', { selector: 'input' })
  await userEvent.keyboard('{Escape}')
  expect(await screen.findByLabelText('Account password')).toHaveValue('')
  expect(screen.getByLabelText('TOTP secret')).toHaveValue('')
})

it.each([
  { id: 7, role: 10 },
  { id: 1, role: 10 },
])(
  'does not display previous privileged rows after switching to administrator $id',
  async (user) => {
    renderPage()
    expect(await screen.findByText('owner@example.com')).toBeVisible()
    const originalGet = vi.mocked(api.get).getMockImplementation()
    if (!originalGet) throw new Error('GET fixture is unavailable')
    vi.mocked(api.get).mockImplementation((url, config) => {
      if (url === '/api/gpt-accounts') return new Promise(() => undefined)
      return originalGet(url, config)
    })
    act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ ...user, username: 'another-admin' })
    )
    expect(screen.queryByText('owner@example.com')).not.toBeInTheDocument()
  }
)

it('cancels pending verification and clears the editor when the administrator changes', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Sign in Work account' })
  )
  await screen.findByLabelText('Password', { selector: 'input' })
  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 7, username: 'another-admin', role: 10 })
  )
  expect(
    screen.queryByLabelText('Password', { selector: 'input' })
  ).not.toBeInTheDocument()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/login',
    expect.anything(),
    expect.anything()
  )
})

async function startBrowserAuthorization() {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  if (!originalGet) throw new Error('GET fixture is unavailable')
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/gpt-accounts/1/oauth') {
      return {
        data: {
          success: true,
          data: {
            authorization_url:
              'https://auth.openai.com/oauth/authorize?state=expected',
            expires_at: Date.now() / 1000 + 600,
          },
        },
      }
    }
    return originalGet(url, config)
  })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', {
      name: 'Browser sign-in for Work account',
    })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Generate sign-in link' })
  )
  await verify()
  await screen.findByLabelText('Callback URL')
}

it('rejects a callback for a different browser authorization before submitting it', async () => {
  await startBrowserAuthorization()
  await userEvent.type(
    screen.getByLabelText('Callback URL'),
    'http://localhost:1455/auth/callback?code=one-use-code&state=unrelated'
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Complete authorization' })
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Paste the complete localhost callback URL containing code and state.'
  )
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/callback',
    expect.anything(),
    expect.anything()
  )
})

it('clears a spent callback after server failure and requires a new authorization link', async () => {
  await startBrowserAuthorization()
  vi.mocked(api.post).mockResolvedValueOnce({
    data: { success: false, code: 'authorization_failed' },
  })
  await userEvent.type(
    screen.getByLabelText('Callback URL'),
    'http://localhost:1455/auth/callback?code=one-use-code&state=expected'
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Complete authorization' })
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Authorization failed. Complete browser sign-in again.'
  )
  expect(screen.queryByLabelText('Callback URL')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('link', { name: 'Open official sign-in' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Generate sign-in link' })
  ).toBeEnabled()
})

it('discards the previous link and callback when a replacement authorization is cancelled', async () => {
  await startBrowserAuthorization()
  await userEvent.type(
    screen.getByLabelText('Callback URL'),
    'http://localhost:1455/auth/callback?code=old-code&state=expected'
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Generate sign-in link' })
  )
  await screen.findByLabelText('Password', { selector: 'input' })
  await userEvent.keyboard('{Escape}')
  await screen.findByRole('dialog', { name: 'Complete browser sign-in' })
  expect(screen.queryByLabelText('Callback URL')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('link', { name: 'Open official sign-in' })
  ).not.toBeInTheDocument()
})

it('discards an expired authorization without submitting its callback', async () => {
  await startBrowserAuthorization()
  const expiredTime = Date.now() + 700_000
  vi.spyOn(Date, 'now').mockReturnValue(expiredTime)
  await userEvent.type(
    screen.getByLabelText('Callback URL'),
    'http://localhost:1455/auth/callback?code=expired-code&state=expected'
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Complete authorization' })
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The authorization link is invalid or expired. Generate a new link.'
  )
  expect(screen.queryByLabelText('Callback URL')).not.toBeInTheDocument()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/callback',
    expect.anything(),
    expect.anything()
  )
})

it('explains a disabled provider account without disclosing provider details', async () => {
  items = [
    {
      ...account,
      status: 'needs_attention',
      last_error_code: 'account_disabled',
    },
  ]
  renderPage()
  expect(
    await screen.findByText('The upstream account is unavailable.')
  ).toBeVisible()
})

it('shows a transient retry message when the sign-in worker is busy', async () => {
  items = [{ ...account, status: 'error', last_error_code: 'worker_busy' }]
  renderPage()
  expect(await screen.findByText('Please try again later.')).toBeVisible()
})

it('submits blank replacement credentials when saving existing account settings', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: account },
  })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  await verify()
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/gpt-accounts/1',
      expect.objectContaining({ password: '', totp_secret: '' }),
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'proof' },
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.update',
      context: { account_id: 1 },
    }),
    expect.anything()
  )
})

it('keeps routing settings read-only after a channel is associated', async () => {
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  expect(screen.getByLabelText('Group')).toHaveAttribute('readonly')
  expect(screen.getByLabelText('Models')).toHaveAttribute('readonly')
  expect(
    screen.getByText('Manage routing settings in the associated channel.')
  ).toBeVisible()
})

it('allows sign-in for an account awaiting authorization after settings were saved', async () => {
  items = [{ ...account, status: 'pending' }]
  renderPage()
  expect(
    await screen.findByRole('button', { name: 'Sign in Work account' })
  ).toBeEnabled()
  expect(
    screen.getByRole('button', { name: 'Browser sign-in for Work account' })
  ).toBeEnabled()
  expect(
    screen.getByRole('button', { name: 'Refresh credentials for Work account' })
  ).toBeEnabled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Sign in Work account' })
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/1/login',
      {},
      expect.anything()
    )
  )
})

it('does not poll an account awaiting authorization', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  items = [{ ...account, status: 'pending' }]
  renderPage()
  await screen.findByText('owner@example.com')
  vi.mocked(api.get).mockClear()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000)
  })
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/gpt-accounts',
    expect.anything()
  )
})

it('shows a rejected deletion inside the confirmation and keeps the account visible', async () => {
  vi.spyOn(api, 'delete').mockResolvedValue({
    data: { success: false, code: 'account_changed' },
  })
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Delete Work account' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
  await verify()
  const confirmation = await screen.findByRole('alertdialog', {
    name: 'Delete GPT account?',
  })
  expect(await within(confirmation).findByRole('alert')).toHaveTextContent(
    'The account changed. Refresh and try again.'
  )
  expect(screen.getByText('owner@example.com')).toBeInTheDocument()
})

it.each([
  {
    field: 'Account password',
    value: 'p'.repeat(1025),
    message: 'Account password is too long',
  },
  {
    field: 'Account password',
    value: '密'.repeat(342),
    message: 'Account password is too long',
  },
  {
    field: 'TOTP secret',
    value: 'A'.repeat(129),
    message: 'Enter a Base32 TOTP secret',
  },
  {
    field: 'TOTP secret',
    value: 'JBSWY3DPEHPK3PXP=',
    message: 'Enter a Base32 TOTP secret',
  },
  {
    field: 'TOTP secret',
    value: 'A'.repeat(17),
    message: 'Enter a Base32 TOTP secret',
  },
  {
    field: 'TOTP secret',
    value: `${'A'.repeat(17)}B`,
    message: 'Enter a Base32 TOTP secret',
  },
])(
  'rejects invalid $field before requesting verification ($value.length characters)',
  async ({ field, value, message }) => {
    const user = userEvent.setup()
    renderPage()
    await user.click(
      await screen.findByRole('button', { name: 'Edit Work account' })
    )
    await user.click(screen.getByLabelText(field))
    await user.paste(value)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(message)).toBeVisible()
    expect(screen.getByLabelText(field)).toHaveAttribute('aria-invalid', 'true')
    expect(api.get).not.toHaveBeenCalledWith(
      '/api/verify/methods',
      expect.anything()
    )
  }
)

it('normalizes a valid spaced lowercase TOTP seed and accepts a password at the byte limit', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: account },
  })
  renderPage()
  await user.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  await user.click(screen.getByLabelText('Account password'))
  const password = `${'密'.repeat(341)}a`
  await user.paste(password)
  await user.click(screen.getByLabelText('TOTP secret'))
  await user.paste(' jbsw y3dp ehpk 3pxp ')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await verify()
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/gpt-accounts/1',
      expect.objectContaining({ password, totp_secret: 'JBSWY3DPEHPK3PXP' }),
      expect.anything()
    )
  )
})

it.each([
  {
    status: 'error' as const,
    auto_relogin: true,
    next_retry_at: 1_700_000_300,
    visible: true,
  },
  {
    status: 'needs_attention' as const,
    auto_relogin: true,
    next_retry_at: 1_700_000_300,
    visible: false,
  },
  {
    status: 'error' as const,
    auto_relogin: false,
    next_retry_at: 1_700_000_300,
    visible: false,
  },
  {
    status: 'error' as const,
    auto_relogin: true,
    next_retry_at: 1_699_999_700,
    visible: false,
  },
])(
  'shows a retry time only for eligible automatic retries ($status, auto=$auto_relogin, at=$next_retry_at)',
  async ({ visible, ...retry }) => {
    vi.spyOn(Date, 'now').mockReturnValue(1_700_000_000_000)
    items = [{ ...account, ...retry }]
    renderPage()
    await screen.findByText('owner@example.com')
    const retryTime = screen.queryByText(formatTimestamp(retry.next_retry_at))
    if (visible) expect(retryTime).toBeVisible()
    else expect(retryTime).not.toBeInTheDocument()
  }
)

async function openTotpTab() {
  await userEvent.click(
    await screen.findByRole('tab', { name: '2FA management' })
  )
  return screen.findByRole('tabpanel', { name: '2FA management' })
}

async function openTotpSave() {
  renderPage()
  const panel = await openTotpTab()
  await userEvent.click(
    within(panel).getByRole('button', {
      name: 'Save replacement for Work account',
    })
  )
  return screen.findByRole('dialog', { name: 'Save replacement secret' })
}

async function revealTotp() {
  const originalPost = vi.mocked(api.post).getMockImplementation()
  if (!originalPost) throw new Error('POST fixture is unavailable')
  vi.mocked(api.post).mockImplementation(async (url, body, config) => {
    if (url === '/api/gpt-accounts/1/totp/reveal') {
      return {
        data: {
          success: true,
          data: {
            totp_secret: 'JBSWY3DPEHPK3PXP',
            expires_at: Date.now() / 1000 + 30,
          },
        },
      }
    }
    return originalPost(url, body, config)
  })
  renderPage()
  const panel = await openTotpTab()
  await userEvent.click(
    within(panel).getByRole('button', {
      name: 'Reveal secret for Work account',
    })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reveal secret' }))
  await verify()
  return screen.findByText('JBSWY3DPEHPK3PXP')
}

it('2FA tab shows masked stored-secret metadata without claiming upstream verification', async () => {
  items = [
    {
      ...account,
      totp_version: 2,
      totp_updated_at: 1_700_000_000,
      totp_status: 'operator_confirmed',
    },
  ]
  renderPage()
  const panel = await openTotpTab()
  expect(screen.getByRole('tab', { name: '2FA management' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(within(panel).getByText('owner@example.com')).toBeVisible()
  expect(within(panel).getByText('Codex')).toBeVisible()
  expect(within(panel).getByText('••••••••')).toBeVisible()
  expect(within(panel).getByText('Operator confirmed')).toBeVisible()
  expect(within(panel).getByText('2')).toBeVisible()
  expect(within(panel).getByText(formatTimestamp(1_700_000_000))).toBeVisible()
  expect(
    within(panel).getByText(
      'Complete authenticator binding on the official OpenAI website before saving. This gateway does not verify upstream binding.'
    )
  ).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
})

it('2FA actions honor server management permission and disable reveal for absent secrets', async () => {
  items = [
    { ...account, can_manage: false },
    { ...account, id: 2, name: 'Empty account', has_totp: false },
  ]
  renderPage()
  const panel = await openTotpTab()
  expect(
    within(panel).getByRole('button', {
      name: 'Save replacement for Work account',
    })
  ).toBeDisabled()
  expect(
    within(panel).getByRole('button', {
      name: 'Reveal secret for Work account',
    })
  ).toBeDisabled()
  expect(
    within(panel).getByRole('button', {
      name: 'Reveal secret for Empty account',
    })
  ).toBeDisabled()
  expect(
    within(panel).getByRole('button', {
      name: 'Save replacement for Empty account',
    })
  ).toBeEnabled()
  expect(within(panel).getByText('Saved')).toBeVisible()
})

it('requires a canonical seed and explicit completed-binding confirmation before verification', async () => {
  const dialog = await openTotpSave()
  const input = within(dialog).getByLabelText('Replacement TOTP secret')
  expect(input).toHaveValue('')
  expect(input).toHaveAttribute('type', 'password')
  await userEvent.type(input, 'JBSWY3DPEHPK3PXP')
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Save replacement secret' })
  )
  expect(
    await screen.findByText(
      'Confirm that official authenticator binding is complete.'
    )
  ).toBeVisible()
  await userEvent.click(
    screen.getByRole('checkbox', {
      name: 'I completed binding this secret on the official OpenAI website.',
    })
  )
  await userEvent.clear(input)
  await userEvent.type(input, 'AAAAAAAAAAAAAAAAAB')
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Save replacement secret' })
  )
  expect(await screen.findByText('Enter a Base32 TOTP secret')).toBeVisible()
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/verify/methods',
    expect.anything()
  )
})

it('saves a normalized replacement only with account-scoped proof and refreshes safe metadata', async () => {
  const originalPost = vi.mocked(api.post).getMockImplementation()
  if (!originalPost) throw new Error('POST fixture is unavailable')
  vi.mocked(api.post).mockImplementation(async (url, body, config) => {
    if (url === '/api/gpt-accounts/1/totp') {
      items = [
        {
          ...account,
          totp_status: 'operator_confirmed',
          totp_version: 1,
          totp_updated_at: 1_700_000_000,
        },
      ]
      return { data: { success: true, data: items[0] } }
    }
    return originalPost(url, body, config)
  })
  const dialog = await openTotpSave()
  await userEvent.type(
    within(dialog).getByLabelText('Replacement TOTP secret'),
    ' jbsw y3dp ehpk 3pxp '
  )
  await userEvent.click(
    screen.getByRole('checkbox', {
      name: 'I completed binding this secret on the official OpenAI website.',
    })
  )
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Save replacement secret' })
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/1/totp',
      { totp_secret: 'JBSWY3DPEHPK3PXP', confirmed_bound: true },
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'proof' },
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.totp.update',
      context: { account_id: 1 },
    }),
    expect.anything()
  )
  expect(await screen.findByText('Operator confirmed')).toBeVisible()
  expect(
    screen.queryByRole('dialog', { name: 'Save replacement secret' })
  ).not.toBeInTheDocument()
  expect(
    JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((query) => query.state)
    )
  ).not.toContain('JBSWY3DPEHPK3PXP')
})

it('cancelling replacement verification clears the seed and confirmation without saving', async () => {
  const dialog = await openTotpSave()
  await userEvent.type(
    within(dialog).getByLabelText('Replacement TOTP secret'),
    'JBSWY3DPEHPK3PXP'
  )
  await userEvent.click(
    screen.getByRole('checkbox', {
      name: 'I completed binding this secret on the official OpenAI website.',
    })
  )
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Save replacement secret' })
  )
  await screen.findByLabelText('Password', { selector: 'input' })
  await userEvent.keyboard('{Escape}')
  expect(await screen.findByLabelText('Replacement TOTP secret')).toHaveValue(
    ''
  )
  expect(
    screen.getByRole('checkbox', {
      name: 'I completed binding this secret on the official OpenAI website.',
    })
  ).not.toBeChecked()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/totp',
    expect.anything(),
    expect.anything()
  )
})

it('reveals only after scoped proof and never puts the secret in query or mutation cache', async () => {
  const mutationStates: unknown[] = []
  const unsubscribe = client
    .getMutationCache()
    .subscribe((event) => mutationStates.push(event.mutation?.state))
  await revealTotp()
  unsubscribe()
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.totp.read',
      context: { account_id: 1 },
    }),
    expect.anything()
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/gpt-accounts/1/totp/reveal',
    {},
    expect.objectContaining({
      singleUseAuthorization: true,
      headers: { 'X-Security-Proof': 'proof' },
    })
  )
  const dialog = screen.getByRole('dialog', { name: 'Current TOTP secret' })
  expect(within(dialog).getByText('JBSWY3DPEHPK3PXP')).toBeVisible()
  expect(
    within(dialog).getByRole('button', { name: 'Copy to clipboard' })
  ).toBeVisible()
  expect(JSON.stringify(mutationStates)).not.toContain('JBSWY3DPEHPK3PXP')
  expect(
    mutationStates.every(
      (state) =>
        typeof (state as { variables?: unknown } | undefined)?.variables !==
        'function'
    )
  ).toBe(true)
  expect(
    JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((query) => query.state)
    )
  ).not.toContain('JBSWY3DPEHPK3PXP')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Done' }))
  expect(screen.queryByText('JBSWY3DPEHPK3PXP')).not.toBeInTheDocument()
})

it('removes a revealed secret at its 30-second deadline', async () => {
  const originalPost = vi.mocked(api.post).getMockImplementation()
  if (!originalPost) throw new Error('POST fixture is unavailable')
  vi.mocked(api.post).mockImplementation(async (url, body, config) => {
    if (url === '/api/gpt-accounts/1/totp/reveal') {
      return {
        data: {
          success: true,
          data: {
            totp_secret: 'JBSWY3DPEHPK3PXP',
            expires_at: Date.now() / 1000 + 30,
          },
        },
      }
    }
    return originalPost(url, body, config)
  })
  renderPage()
  await openTotpTab()
  await userEvent.click(
    screen.getByRole('button', { name: 'Reveal secret for Work account' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reveal secret' }))
  await userEvent.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'admin-password'
  )
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  await act(async () =>
    fireEvent.click(screen.getByRole('button', { name: 'Verify' }))
  )
  expect(screen.getByText('JBSWY3DPEHPK3PXP')).toBeVisible()
  await act(async () => vi.advanceTimersByTimeAsync(30_001))
  expect(screen.queryByText('JBSWY3DPEHPK3PXP')).not.toBeInTheDocument()
})

it.each(['hidden', 'role', 'session'] as const)(
  'clears a revealed secret when %s changes',
  async (kind) => {
    await revealTotp()
    act(() => {
      if (kind === 'hidden') {
        vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
        document.dispatchEvent(new Event('visibilitychange'))
      } else if (kind === 'role') {
        useAuthStore
          .getState()
          .auth.setUser({ id: 1, username: 'admin', role: 10 })
      } else {
        useAuthStore.setState((state) => ({
          auth: {
            ...state.auth,
            session: {
              sid: 'replacement',
              current: true,
              login_method: 'password',
              ip: '',
              user_agent: '',
              created_at: 0,
              last_active_at: 0,
              expires_at: 0,
            },
          },
        }))
      }
    })
    expect(screen.queryByText('JBSWY3DPEHPK3PXP')).not.toBeInTheDocument()
  }
)

it('hiding a pending reveal aborts the request and discards a late response', async () => {
  let resolveReveal: (value: unknown) => void = () => undefined
  const originalPost = vi.mocked(api.post).getMockImplementation()
  if (!originalPost) throw new Error('POST fixture is unavailable')
  vi.mocked(api.post).mockImplementation(async (url, body, config) => {
    if (url === '/api/gpt-accounts/1/totp/reveal') {
      return new Promise((resolve) => {
        resolveReveal = resolve
      })
    }
    return originalPost(url, body, config)
  })
  renderPage()
  await openTotpTab()
  await userEvent.click(
    screen.getByRole('button', { name: 'Reveal secret for Work account' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reveal secret' }))
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/1/totp/reveal',
      {},
      expect.anything()
    )
  )
  const signal = vi
    .mocked(api.post)
    .mock.calls.find(
      ([url]) => url === '/api/gpt-accounts/1/totp/reveal'
    )?.[2]?.signal
  act(() => {
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(signal?.aborted).toBe(true)
  await act(async () =>
    resolveReveal({
      data: {
        success: true,
        data: {
          totp_secret: 'JBSWY3DPEHPK3PXP',
          expires_at: Date.now() / 1000 + 30,
        },
      },
    })
  )
  expect(screen.queryByText('JBSWY3DPEHPK3PXP')).not.toBeInTheDocument()
})

it.each([
  {
    code: 'totp_confirmation_required',
    message: 'Confirm that official authenticator binding is complete.',
  },
  {
    code: 'totp_secret_not_found',
    message: 'No TOTP secret is stored for this account.',
  },
])(
  'shows a safe TOTP error for $code without retaining submitted secrets in cache',
  async ({ code, message }) => {
    const originalPost = vi.mocked(api.post).getMockImplementation()
    if (!originalPost) throw new Error('POST fixture is unavailable')
    vi.mocked(api.post).mockImplementation(async (url, body, config) => {
      if (url === '/api/gpt-accounts/1/totp') {
        return { data: { success: false, code } }
      }
      return originalPost(url, body, config)
    })
    const mutationStates: unknown[] = []
    const unsubscribe = client
      .getMutationCache()
      .subscribe((event) => mutationStates.push(event.mutation?.state))
    const dialog = await openTotpSave()
    await userEvent.type(
      within(dialog).getByLabelText('Replacement TOTP secret'),
      'JBSWY3DPEHPK3PXP'
    )
    await userEvent.click(
      screen.getByRole('checkbox', {
        name: 'I completed binding this secret on the official OpenAI website.',
      })
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Save replacement secret' })
    )
    await verify()
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(screen.getByLabelText('Replacement TOTP secret')).toHaveValue('')
    unsubscribe()
    expect(JSON.stringify(mutationStates)).not.toContain('JBSWY3DPEHPK3PXP')
    expect(
      mutationStates.every(
        (state) =>
          typeof (state as { variables?: unknown } | undefined)?.variables !==
          'function'
      )
    ).toBe(true)
  }
)

it('cancelling reveal verification never fetches a secret', async () => {
  renderPage()
  await openTotpTab()
  await userEvent.click(
    screen.getByRole('button', { name: 'Reveal secret for Work account' })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reveal secret' }))
  await screen.findByLabelText('Password', { selector: 'input' })
  await userEvent.keyboard('{Escape}')
  expect(
    await screen.findByRole('dialog', { name: 'Current TOTP secret' })
  ).toBeVisible()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/totp/reveal',
    expect.anything(),
    expect.anything()
  )
})

it('submits a masked account line in one step with normalized credentials and the exact password after verification', async () => {
  const user = userEvent.setup()
  const password = '  password----with delimiter  '
  const line = `MixedCase@example.com----${password}----jbsw y3dp ehpk 3pxp`
  const mutationStates: unknown[] = []
  const unsubscribe = client.getMutationCache().subscribe((event) => {
    mutationStates.push(event.mutation?.state)
  })
  renderPage()
  await user.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  const input = screen.getByLabelText('Account line')
  expect(input).toHaveAttribute('type', 'password')
  await user.click(input)
  await user.paste(line)
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(api.post).not.toHaveBeenCalled()
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts',
      {
        name: 'mixedcase@example.com',
        email: 'mixedcase@example.com',
        password,
        totp_secret: 'JBSWY3DPEHPK3PXP',
        totp_provider: '2fa_cn',
        auto_relogin: true,
        group: 'default',
        models: '',
        channel_id: undefined,
        gateway_id: 0,
      },
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'proof' },
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.create',
      context: { email: 'mixedcase@example.com' },
    }),
    expect.anything()
  )
  unsubscribe()
  const cache = JSON.stringify({
    queries: client
      .getQueryCache()
      .getAll()
      .map((query) => query.state),
    mutations: mutationStates,
  })
  expect(cache).not.toContain(line)
  expect(cache).not.toContain(password)
  expect(cache).not.toContain('JBSWY3DPEHPK3PXP')
})

it.each([
  ['missing delimiters', 'owner@example.com'],
  ['missing email', '----password----JBSWY3DPEHPK3PXP'],
  ['missing password', 'owner@example.com--------JBSWY3DPEHPK3PXP'],
  ['missing seed', 'owner@example.com----password----'],
  ['invalid email', 'owner----password----JBSWY3DPEHPK3PXP'],
  ['invalid seed', 'owner@example.com----password----AAAAAAAAAAAAAAAAAB'],
  [
    'oversized password',
    `owner@example.com----${'密'.repeat(342)}----JBSWY3DPEHPK3PXP`,
  ],
  [
    'multiple lines',
    'owner@example.com----password----JBSWY3DPEHPK3PXP\nother@example.com----password----JBSWY3DPEHPK3PXP',
  ],
])(
  'rejects an account line with %s before verification or account mutation',
  async (_reason, line) => {
    const user = userEvent.setup()
    renderPage()
    await user.click(
      await screen.findByRole('button', { name: 'Add GPT account' })
    )
    await user.click(screen.getByLabelText('Account line'))
    await user.paste(line)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Enter one account as email----password----TOTP secret.'
    )
    expect(screen.getByLabelText('Account line')).toHaveAttribute(
      'aria-invalid',
      'true'
    )
    expect(api.get).not.toHaveBeenCalledWith(
      '/api/verify/methods',
      expect.anything()
    )
    expect(api.post).not.toHaveBeenCalled()
  }
)

it('keeps a supplied account name when applying a line and clears imported secrets after canceled verification', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  await user.type(screen.getByLabelText('Name'), 'My account')
  await user.click(screen.getByLabelText('Account line'))
  await user.paste('owner@example.com----password----JBSWY3DPEHPK3PXP')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await screen.findByLabelText('Password', { selector: 'input' })
  await user.keyboard('{Escape}')
  expect(await screen.findByLabelText('Account password')).toHaveValue('')
  expect(screen.getByLabelText('Name')).toHaveValue('My account')
  expect(screen.getByLabelText('TOTP secret')).toHaveValue('')
  await user.click(screen.getByRole('tab', { name: 'Paste account' }))
  expect(screen.getByLabelText('Account line')).toHaveValue('')
  expect(api.post).not.toHaveBeenCalled()
})

it.each(['cancel', 'identity'] as const)(
  'discards a pasted account line after %s closes its editor',
  async (reason) => {
    const user = userEvent.setup()
    renderPage()
    await user.click(
      await screen.findByRole('button', { name: 'Add GPT account' })
    )
    await user.type(
      screen.getByLabelText('Account line'),
      'owner@example.com----password----JBSWY3DPEHPK3PXP'
    )
    if (reason === 'cancel') {
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
    } else {
      act(() =>
        useAuthStore
          .getState()
          .auth.setUser({ id: 7, username: 'other', role: 100 })
      )
    }
    await user.click(
      await screen.findByRole('button', { name: 'Add GPT account' })
    )
    expect(screen.getByLabelText('Account line')).toHaveValue('')
    expect(api.post).not.toHaveBeenCalled()
  }
)

it('opens the manual authenticator handoff from the main account row without mutating provider MFA', async () => {
  const user = userEvent.setup()
  renderPage()
  const action = await screen.findByRole('button', {
    name: 'Change 2FA for Work account',
  })
  expect(action).toHaveTextContent('Change 2FA')
  await user.click(action)
  const dialog = await screen.findByRole('dialog', {
    name: 'Save replacement secret',
  })
  const link = within(dialog).getByRole('link', {
    name: 'Open ChatGPT security settings',
  })
  expect(link).toHaveAttribute('href', 'https://chatgpt.com/#settings/Security')
  expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  expect(
    within(dialog).getByText(
      'On the official website, replace the authenticator yourself. After binding the new secret, return here to save it.'
    )
  ).toBeVisible()
  expect(
    within(dialog).getByRole('button', { name: 'Save replacement secret' })
  ).toBeVisible()
  expect(
    within(dialog).getByRole('checkbox', {
      name: 'I completed binding this secret on the official OpenAI website.',
    })
  ).not.toBeChecked()
  expect(api.post).not.toHaveBeenCalled()
})

it.each([
  { can_manage: false, status: 'ready' as const },
  { can_manage: true, status: 'logging_in' as const },
])(
  'disables main-row 2FA replacement for permission=$can_manage and status=$status',
  async (state) => {
    items = [{ ...account, ...state }]
    renderPage()
    expect(
      await screen.findByRole('button', { name: 'Change 2FA for Work account' })
    ).toBeDisabled()
  }
)

it('shows the third-party secret disclosure with 2fa.cn selected for pasted accounts and allows choosing local codes', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  expect(
    screen.getByRole('radiogroup', { name: 'Sign-in code source' })
  ).toBeVisible()
  expect(screen.getByRole('radio', { name: '2fa.cn' })).toBeChecked()
  expect(
    screen.getByText(
      '2fa.cn receives this account’s TOTP secret to generate sign-in codes.'
    )
  ).toBeVisible()
  await user.click(screen.getByRole('radio', { name: 'Generate locally' }))
  expect(screen.getByRole('radio', { name: 'Generate locally' })).toBeChecked()
  expect(
    screen.queryByText(
      '2fa.cn receives this account’s TOTP secret to generate sign-in codes.'
    )
  ).not.toBeInTheDocument()
  await user.click(screen.getByLabelText('Account line'))
  await user.paste('owner@example.com----password----JBSWY3DPEHPK3PXP')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts',
      expect.objectContaining({ totp_provider: 'local' }),
      expect.anything()
    )
  )
})

it.each([
  { stored: undefined, selected: 'Generate locally', expected: 'local' },
  { stored: '' as const, selected: 'Generate locally', expected: 'local' },
  { stored: '2fa_cn' as const, selected: '2fa.cn', expected: '2fa_cn' },
])(
  'editing preserves the saved TOTP provider or defaults a legacy value ($stored) to local',
  async (provider) => {
    const user = userEvent.setup()
    items = [{ ...account, totp_provider: provider.stored }]
    vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, data: items[0] },
    })
    renderPage()
    await user.click(
      await screen.findByRole('button', { name: 'Edit Work account' })
    )
    expect(screen.getByRole('radio', { name: provider.selected })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await verify()
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/api/gpt-accounts/1',
        expect.objectContaining({ totp_provider: provider.expected }),
        expect.anything()
      )
    )
  }
)

it.each([
  [
    'captcha_required',
    'The sign-in page requires a CAPTCHA. Complete it through browser sign-in.',
  ],
  [
    'email_verification_required',
    'The sign-in page requires email verification. Complete it through browser sign-in.',
  ],
  [
    'consent_required',
    'The sign-in page requires your consent. Complete it through browser sign-in.',
  ],
  [
    'additional_verification_required',
    'The sign-in page requires additional verification. Complete it through browser sign-in.',
  ],
  [
    'login_navigation_blocked',
    'The sign-in page blocked navigation. Try browser sign-in.',
  ],
  [
    'login_navigation_failed',
    'Could not open the sign-in page. Try again or use browser sign-in.',
  ],
  [
    'email_submission_failed',
    'Could not submit the account email on the sign-in page.',
  ],
  [
    'password_submission_failed',
    'Could not submit the account password on the sign-in page.',
  ],
  [
    'totp_submission_failed',
    'Could not submit the authenticator code on the sign-in page.',
  ],
  [
    'email_step_timeout',
    'The sign-in page did not continue after the email step. Try again.',
  ],
  [
    'password_step_timeout',
    'The sign-in page did not continue after the password step. Try again.',
  ],
  [
    'totp_step_timeout',
    'The sign-in page did not continue after the authenticator code step. Try again.',
  ],
  [
    'login_screen_unrecognized',
    'The sign-in page could not be recognized. Try browser sign-in.',
  ],
  [
    'totp_secret_required',
    'Add this account’s TOTP secret to generate sign-in codes.',
  ],
  [
    'totp_provider_unavailable',
    'The sign-in code provider is unavailable. Try again.',
  ],
  ['totp_provider_timeout', 'The sign-in code provider timed out. Try again.'],
  [
    'totp_provider_invalid_response',
    'The sign-in code provider returned an invalid code. Try again.',
  ],
  [
    'invalid_totp_code',
    'The sign-in page rejected the authenticator code. Check the saved secret and try again.',
  ],
])(
  'shows the specific sign-in failure for %s without changing the administrator session',
  async (code, message) => {
    items = [{ ...account, status: 'needs_attention', last_error_code: code }]
    renderPage()
    expect(await screen.findByText(message)).toBeVisible()
    expect(useAuthStore.getState().auth.user?.id).toBe(1)
  }
)

it('lets administrators choose a configured gateway during one-line creation without exposing gateway management', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  const user = userEvent.setup()
  renderPage()
  expect(
    screen.queryByRole('tab', { name: 'Gateways' })
  ).not.toBeInTheDocument()
  await user.click(
    await screen.findByRole('button', { name: 'Add GPT account' })
  )
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Account destination' }),
    '5'
  )
  expect(
    screen.getByText(
      'Accounts added to this gateway share its channel. Requests may use any available account in the gateway.'
    )
  ).toBeVisible()
  expect(
    screen.queryByLabelText('Existing Codex channel ID')
  ).not.toBeInTheDocument()
  await user.type(
    screen.getByLabelText('Account line'),
    'pool@example.com----password----JBSWY3DPEHPK3PXP'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts',
      expect.objectContaining({ gateway_id: 5, channel_id: undefined }),
      expect.anything()
    )
  )
})

it('shows gateway channel and sync status and retries import only after account-scoped verification', async () => {
  items = [
    {
      ...account,
      channel_id: undefined,
      gateway_id: 5,
      gateway_name: 'CPA pool',
      gateway_provider: 'cpa',
      gateway_channel_id: 23,
      gateway_channel_name: 'Shared CPA channel',
      gateway_sync_status: 'error',
      gateway_error_code: 'gateway_unavailable',
    },
  ]
  const user = userEvent.setup()
  renderPage()
  expect(await screen.findByText('Shared CPA channel')).toBeVisible()
  expect(screen.getByText('Gateway synchronization failed')).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Sync gateway for Work account' })
  )
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-accounts/1/sync',
    expect.anything(),
    expect.anything()
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-accounts/1/sync',
      {},
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'proof' },
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account.gateway.sync',
      context: { account_id: 1 },
    }),
    expect.anything()
  )
})

it.each(['cpa', 'sub2api'] as const)(
  'refreshes a %s gateway account without a direct channel after scoped verification',
  async (provider) => {
    items = [
      {
        ...account,
        channel_id: undefined,
        gateway_id: 5,
        gateway_provider: provider,
        gateway_sync_status: 'synced',
      },
    ]
    renderPage()
    const refresh = await screen.findByRole('button', {
      name: 'Refresh credentials for Work account',
    })
    expect(refresh).toBeEnabled()
    await userEvent.click(refresh)
    expect(api.post).not.toHaveBeenCalledWith(
      '/api/gpt-accounts/1/refresh',
      expect.anything(),
      expect.anything()
    )
    await verify()
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/gpt-accounts/1/refresh',
        {},
        expect.objectContaining({
          singleUseAuthorization: true,
          headers: { 'X-Security-Proof': 'proof' },
        })
      )
    )
    expect(api.post).toHaveBeenCalledWith(
      '/api/verify',
      expect.objectContaining({
        scope: 'gpt_account.refresh',
        context: { account_id: 1 },
      }),
      expect.anything()
    )
  }
)

it('keeps the account destination immutable when editing a gateway account', async () => {
  items = [
    {
      ...account,
      channel_id: undefined,
      gateway_id: 5,
      gateway_name: 'CPA pool',
    },
  ]
  renderPage()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Edit Work account' })
  )
  expect(
    screen.getByRole('combobox', { name: 'Account destination' })
  ).toBeDisabled()
  expect(
    screen.getByRole('combobox', { name: 'Account destination' })
  ).toHaveValue('5')
  expect(
    screen.queryByLabelText('Existing Codex channel ID')
  ).not.toBeInTheDocument()
})

it('creates a gateway with masked keys only after name-scoped verification and keeps keys out of caches', async () => {
  const user = userEvent.setup()
  const mutationStates: unknown[] = []
  const unsubscribe = client
    .getMutationCache()
    .subscribe((event) => mutationStates.push(event.mutation?.state))
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Add gateway' }))
  const dialog = screen.getByRole('dialog', { name: 'Add gateway' })
  await user.type(within(dialog).getByLabelText('Name'), 'Second pool')
  await user.type(
    within(dialog).getByLabelText('Management URL'),
    'https://admin.example.com'
  )
  await user.type(
    within(dialog).getByLabelText('Relay URL'),
    'https://relay.example.com'
  )
  expect(within(dialog).getByLabelText('Management key')).toHaveAttribute(
    'type',
    'password'
  )
  expect(within(dialog).getByLabelText('Relay key')).toHaveAttribute(
    'type',
    'password'
  )
  await user.type(
    within(dialog).getByLabelText('Management key'),
    'gateway-management-secret'
  )
  await user.type(
    within(dialog).getByLabelText('Relay key'),
    'gateway-relay-secret'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/gpt-account-gateways',
    expect.anything(),
    expect.anything()
  )
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-account-gateways',
      expect.objectContaining({
        name: 'Second pool',
        provider: 'cpa',
        management_version: 'v8',
        management_key: 'gateway-management-secret',
        relay_key: 'gateway-relay-secret',
      }),
      expect.objectContaining({ singleUseAuthorization: true })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account_gateway.create',
      context: { name: 'Second pool' },
    }),
    expect.anything()
  )
  unsubscribe()
  const cache = JSON.stringify({
    queries: client
      .getQueryCache()
      .getAll()
      .map((query) => query.state),
    mutations: mutationStates,
  })
  expect(cache).not.toContain('gateway-management-secret')
  expect(cache).not.toContain('gateway-relay-secret')
})

it('imports a direct CPA or Sub2API channel JSON only after scoped verification', async () => {
  const user = userEvent.setup()
  const configJSON =
    '{"provider":"sub2api","name":"测试渠道","base_url":"https://sub2api.example/v1","api_key":"relay-secret","models":["gpt-5"]}'
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Import channel JSON' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Import CPA/Sub2API channel JSON',
  })
  fireEvent.change(within(dialog).getByLabelText('Channel JSON'), {
    target: { value: configJSON },
  })
  await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
  await verify()
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-account-gateways/import-channel-json',
      { config_json: configJSON },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account_gateway.channel_import',
      context: {
        config_sha256:
          '3477af6d92429ec7f8da7a6c5667abd9571305a70bdfed6227e62fa012a689a0',
      },
    }),
    expect.anything()
  )
})

it('rejects non-object channel JSON before requesting security verification', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Import channel JSON' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Import CPA/Sub2API channel JSON',
  })
  fireEvent.change(within(dialog).getByLabelText('Channel JSON'), {
    target: { value: '[]' },
  })
  await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
  expect(
    await within(dialog).findByText(
      'Paste a valid CPA or Sub2API channel JSON object.'
    )
  ).toBeVisible()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/verify',
    expect.anything(),
    expect.anything()
  )
})

it('abandons channel JSON verification when the page closes during hashing', async () => {
  const user = userEvent.setup()
  let finishDigest!: (digest: ArrayBuffer) => void
  const digest = vi
    .spyOn(globalThis.crypto.subtle, 'digest')
    .mockImplementationOnce(
      () => new Promise<ArrayBuffer>((resolve) => { finishDigest = resolve })
    )
  const page = renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Import channel JSON' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Import CPA/Sub2API channel JSON',
  })
  fireEvent.change(within(dialog).getByLabelText('Channel JSON'), {
    target: {
      value: '{"provider":"cpa","base_url":"https://cpa.example","api_key":"relay-secret"}',
    },
  })
  await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
  await waitFor(() => expect(digest).toHaveBeenCalledOnce())
  page.unmount()
  await act(async () => { finishDigest(new Uint8Array(32).buffer) })
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/verify/methods',
    expect.anything()
  )
  expect(api.post).not.toHaveBeenCalled()
})

it('does not import channel JSON when security verification is cancelled', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Import channel JSON' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Import CPA/Sub2API channel JSON',
  })
  fireEvent.change(within(dialog).getByLabelText('Channel JSON'), {
    target: {
      value: '{"provider":"cpa","base_url":"https://cpa.example","api_key":"relay-secret"}',
    },
  })
  await user.click(within(dialog).getByRole('button', { name: 'Create channel' }))
  const verification = await screen.findByRole('dialog', {
    name: 'Security verification',
  })
  await user.click(within(verification).getByRole('button', { name: 'Cancel' }))
  expect(await screen.findByRole('dialog', {
    name: 'Import CPA/Sub2API channel JSON',
  })).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
})

it('edits gateway display settings without exposing saved keys or changing connection identity', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: gateway },
  })
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(
    await screen.findByRole('button', { name: 'Edit gateway CPA pool' })
  )
  const dialog = screen.getByRole('dialog', { name: 'Edit gateway' })
  expect(within(dialog).getByLabelText('Management key')).toHaveValue('')
  expect(within(dialog).getByLabelText('Relay key')).toHaveValue('')
  expect(within(dialog).getByLabelText('Management URL')).toHaveAttribute(
    'readonly'
  )
  expect(within(dialog).getByLabelText('Relay URL')).toHaveAttribute('readonly')
  expect(
    within(dialog).getByRole('combobox', { name: 'Provider' })
  ).toBeDisabled()
  await user.clear(within(dialog).getByLabelText('Name'))
  await user.type(within(dialog).getByLabelText('Name'), 'Renamed pool')
  await user.click(within(dialog).getByRole('button', { name: 'Save' }))
  await verify()
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/gpt-account-gateways/5',
      expect.objectContaining({
        name: 'Renamed pool',
        management_key: '',
        relay_key: '',
        management_url: gateway.management_url,
        relay_url: gateway.relay_url,
      }),
      expect.anything()
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'gpt_account_gateway.update',
      context: { gateway_id: 5 },
    }),
    expect.anything()
  )
})

it('clears replacement gateway keys when verification is cancelled', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(
    await screen.findByRole('button', { name: 'Edit gateway CPA pool' })
  )
  await user.type(screen.getByLabelText('Management key'), 'replacement-secret')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await screen.findByLabelText('Password', { selector: 'input' })
  await waitFor(() =>
    expect(
      screen.getByRole('dialog', { name: 'Security verification' })
    ).toContainElement(document.activeElement as HTMLElement)
  )
  await user.keyboard('{Escape}')
  expect(await screen.findByLabelText('Management key')).toHaveValue('')
  expect(screen.getByLabelText('Relay key')).toHaveValue('')
  expect(api.post).not.toHaveBeenCalled()
})

it('requires both gateway keys and a valid Sub2API group before requesting verification', async () => {
  const user = userEvent.setup()
  renderPage()
  await user.click(await screen.findByRole('tab', { name: 'Gateways' }))
  await user.click(screen.getByRole('button', { name: 'Add gateway' }))
  await user.type(screen.getByLabelText('Name'), 'Sub2API pool')
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Provider' }),
    'sub2api'
  )
  await user.type(
    screen.getByLabelText('Management URL'),
    'https://admin.example.com'
  )
  await user.type(
    screen.getByLabelText('Relay URL'),
    'https://relay.example.com'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Management key is required')).toBeVisible()
  expect(screen.getByText('Relay key is required')).toBeVisible()
  expect(
    screen.getByText('Choose a Sub2API group ID greater than zero.')
  ).toBeVisible()
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/verify/methods',
    expect.anything()
  )
})
