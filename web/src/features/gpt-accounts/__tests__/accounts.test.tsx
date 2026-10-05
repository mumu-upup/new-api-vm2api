import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
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
  can_manage: true,
  group: 'default',
  models: 'gpt-5.4',
}
let items: GptAccount[]
let client: QueryClient

beforeEach(() => {
  items = [{ ...account }]
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
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
