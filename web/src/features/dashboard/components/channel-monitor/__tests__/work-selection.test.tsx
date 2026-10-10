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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import type {
  ChannelMonitorArtifact,
  ChannelMonitorCard,
  ChannelMonitorData,
  ChannelMonitorQuestion,
} from '@/features/dashboard/types'

import {
  ChannelMonitorDashboard,
  WorkDialog,
} from '../channel-monitor-dashboard'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, params?: { number: number }) =>
      params ? key.replace('{{number}}', String(params.number)) : key,
  }),
}))
it('switches between historical works and questions without merging their results', async () => {
  const base = {
    group: 'OpenAI / vip',
    model: 'm',
    preset_id: 'text-q',
    artifact_type: 'text',
    channel_name: 'Channel',
    duration_ms: 1,
  } as ChannelMonitorArtifact
  const card: ChannelMonitorCard = {
    key: 'OpenAI / vip',
    group: 'OpenAI / vip',
    platform: 'OpenAI',
    models: ['m'],
    question_ids: ['text-q', 'svg-q'],
    interval_minutes: 30,
    latest: 2,
    artifacts: [
      { ...base, id: 2, created_at: 2, content: 'new answer' },
      { ...base, id: 1, created_at: 1, content: 'old answer' },
      {
        ...base,
        id: 3,
        preset_id: 'svg-q',
        artifact_type: 'html',
        created_at: 3,
        content: '```svg\n<svg><circle r="10"/></svg>\n```',
      },
    ],
  }
  const questions = [
    { id: 'text-q', name: 'Text question', matcher: 'none' },
    { id: 'svg-q', name: 'SVG question', matcher: 'none' },
  ] as ChannelMonitorQuestion[]
  render(
    <WorkDialog card={card} questions={questions} open onOpenChange={vi.fn()} />
  )
  expect(screen.getByText('new answer')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Work 2' }))
  expect(screen.getByText('old answer')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'SVG question' }))
  const iframe = screen.getByTitle('检测作品预览')
  expect(iframe).toHaveAttribute('sandbox', 'allow-scripts')
  expect(iframe.getAttribute('srcdoc')).toContain('<svg>')
  expect(
    screen.queryByRole('button', { name: 'Work 2' })
  ).not.toBeInTheDocument()
})

it('shows an empty response message instead of a blank HTML preview', () => {
  const card: ChannelMonitorCard = {
    key: 'OpenAI / vip',
    group: 'OpenAI / vip',
    platform: 'OpenAI',
    models: ['m'],
    question_ids: ['svg-q'],
    interval_minutes: 30,
    latest: 1,
    artifacts: [
      {
        id: 1,
        created_at: 1,
        model: 'm',
        preset_id: 'svg-q',
        artifact_type: 'html',
        content: '   ',
      } as ChannelMonitorArtifact,
    ],
  }
  render(<WorkDialog card={card} questions={[]} open onOpenChange={vi.fn()} />)
  expect(screen.getByText('No response content')).toBeVisible()
  expect(screen.queryByTitle('检测作品预览')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Pause preview' })
  ).not.toBeInTheDocument()
})

it('shows no data instead of healthy for models without monitor results', () => {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  const data: ChannelMonitorData = {
    updated_at: 1,
    range: '90m',
    groups: {},
    models: { tested: true, untested: true },
    questions: [],
    cards: {},
    detail: {
      groups: {},
      artifacts: [
        {
          id: 1,
          model: 'tested',
          status: 'success',
          evaluation: 'passed',
          duration_ms: 1,
        } as ChannelMonitorArtifact,
      ],
    },
  }
  client.setQueryData(['dashboard', 'channel-monitor', '90m', 'all', 'all'], {
    success: true,
    data,
  })
  const view = render(
    <QueryClientProvider client={client}>
      <ChannelMonitorDashboard />
    </QueryClientProvider>
  )
  const untested = within(screen.getByRole('row', { name: /untested/ }))
  expect(untested.getByText('No data available')).toBeVisible()
  expect(untested.queryByText('Healthy')).not.toBeInTheDocument()
  expect(
    within(screen.getByRole('row', { name: /tested 100%/ })).getByText(
      'Healthy'
    )
  ).toBeVisible()
  view.unmount()
  client.clear()
})
