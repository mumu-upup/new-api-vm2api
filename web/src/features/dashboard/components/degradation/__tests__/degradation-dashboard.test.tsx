/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ResponseModelMismatchStats } from '@/features/dashboard/types'

import { DegradationDashboard } from '../degradation-dashboard'

const api = vi.hoisted(() => ({ getResponseModelMismatchStats: vi.fn() }))
vi.mock('@/features/dashboard/api', () => api)
vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: vi.fn() },
}))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: (props: {
    search: { requestId: string }
    title?: string
    children: ReactNode
  }) => (
    <a
      href={`/usage-logs/common?requestId=${props.search.requestId}`}
      title={props.title}
    >
      {props.children}
    </a>
  ),
}))

// Wednesday 2026-09-30 12:00 local time.
const NOW = new Date(2026, 8, 30, 12, 0, 0)

const stats: ResponseModelMismatchStats = {
  summary: {
    start_timestamp: 0,
    end_timestamp: 0,
    retained_since: Math.floor(new Date(2026, 8, 28).getTime() / 1000),
    total_requests: 4,
    mismatch_requests: 3,
    mismatch_quota: 600,
    affected_channels: 1,
    affected_users: 2,
    item_limit: 200,
  },
  channels: [
    {
      channel_id: 7,
      channel_name: 'relay-a',
      total_requests: 4,
      mismatch_requests: 3,
      mismatch_quota: 600,
      last_seen_at: Math.floor(NOW.getTime() / 1000) - 120,
      returned_models: ['claude-3-5-haiku'],
    },
  ],
  pairs: [
    {
      requested_model: 'claude-opus-4',
      returned_model: 'claude-3-5-haiku',
      requests: 3,
      channels: 1,
      last_seen_at: Math.floor(NOW.getTime() / 1000) - 120,
    },
  ],
  trend: [],
  items: [
    {
      id: 1,
      created_at: Math.floor(NOW.getTime() / 1000) - 120,
      request_id: 'req-degraded-1',
      user_id: 5,
      username: 'alice',
      token_name: 'default',
      channel_id: 7,
      channel_name: 'relay-a',
      group: 'default',
      requested_model: 'claude-opus-4',
      upstream_model: 'claude-opus-4',
      returned_model: 'claude-3-5-haiku',
      quota: 200,
      prompt_tokens: 100,
      completion_tokens: 20,
      use_time: 3,
      is_stream: true,
    },
  ],
}

const emptyStats: ResponseModelMismatchStats = {
  ...stats,
  summary: {
    ...stats.summary,
    mismatch_requests: 0,
    mismatch_quota: 0,
    affected_channels: 0,
    affected_users: 0,
  },
  channels: [],
  pairs: [],
  items: [],
}

let client: QueryClient
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
})
afterEach(() => {
  client.clear()
  vi.useRealTimers()
})

function renderDashboard() {
  return render(
    <QueryClientProvider client={client}>
      <DegradationDashboard />
    </QueryClientProvider>
  )
}

describe('DegradationDashboard', () => {
  it('lists degraded channels, substitutions and a usage-log link when requests were degraded', async () => {
    api.getResponseModelMismatchStats.mockResolvedValue({
      success: true,
      data: stats,
    })

    renderDashboard()

    const link = await screen.findByRole('link', { name: 'req-degraded-1' })
    expect(screen.getByText('Degraded Channels')).toBeInTheDocument()
    expect(screen.getByText('Model Substitutions')).toBeInTheDocument()
    expect(screen.getAllByText('75%').length).toBeGreaterThan(0)
    expect(screen.getAllByText('relay-a').length).toBeGreaterThan(0)
    expect(link).toHaveAttribute(
      'href',
      '/usage-logs/common?requestId=req-degraded-1'
    )
    expect(link).toHaveAttribute('title', 'View in usage logs')
  })

  it('shows the empty state instead of the tables when nothing was degraded', async () => {
    api.getResponseModelMismatchStats.mockResolvedValue({
      success: true,
      data: emptyStats,
    })

    renderDashboard()

    expect(await screen.findByText('No degraded requests')).toBeInTheDocument()
    expect(screen.queryByText('Degraded Channels')).not.toBeInTheDocument()
  })

  it('requests the range from local Monday 00:00 after switching to this week', async () => {
    api.getResponseModelMismatchStats.mockResolvedValue({
      success: true,
      data: emptyStats,
    })

    renderDashboard()
    await screen.findByText('No degraded requests')
    expect(api.getResponseModelMismatchStats).toHaveBeenLastCalledWith({
      start_timestamp: Math.floor(NOW.getTime() / 1000) - 24 * 3600,
      end_timestamp: Math.floor(NOW.getTime() / 1000),
    })

    fireEvent.click(screen.getByRole('tab', { name: 'This week' }))

    await waitFor(() =>
      expect(api.getResponseModelMismatchStats).toHaveBeenLastCalledWith({
        start_timestamp: Math.floor(new Date(2026, 8, 28).getTime() / 1000),
        end_timestamp: Math.floor(NOW.getTime() / 1000),
      })
    )
    expect(screen.getByRole('tab', { name: 'This week' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  })

  it('offers a retry when the server rejects the request', async () => {
    api.getResponseModelMismatchStats.mockResolvedValue({
      success: false,
      message: 'forbidden',
    })

    renderDashboard()

    expect(
      await screen.findByRole('button', { name: 'Retry' })
    ).toBeInTheDocument()
    expect(screen.queryByText('Degraded Channels')).not.toBeInTheDocument()
  })
})
