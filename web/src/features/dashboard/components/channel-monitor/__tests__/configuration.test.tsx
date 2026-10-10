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
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import type { ChannelMonitorConfigData } from '@/features/dashboard/types'

import { ChannelMonitorConfigForm } from '../channel-monitor-config'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))
const data: ChannelMonitorConfigData = {
  config: {
    groups: [
      {
        key: 'OpenAI / vip',
        enabled: true,
        question_ids: ['q'],
        interval_minutes: 30,
      },
    ],
    questions: [],
  },
  available_groups: [
    {
      key: 'OpenAI / vip',
      platform: 'OpenAI',
      group: 'vip',
      models: ['m'],
      channel_ids: [1],
    },
  ],
  builtin_questions: [
    {
      id: 'q',
      name: 'Question',
      prompt: 'Answer',
      artifact_type: 'text',
      matcher: 'builtin',
      expected: '21',
    },
  ],
}
it('saves group visibility and an independent frequency', async () => {
  const save = vi.fn()
  render(<ChannelMonitorConfigForm data={data} onSave={save} pending={false} />)
  fireEvent.change(screen.getByRole('spinbutton'), { target: { value: '15' } })
  await userEvent.click(screen.getByRole('checkbox', { name: 'OpenAI / vip' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(save).toHaveBeenCalled())
  expect(save.mock.calls[0][0].groups[0]).toMatchObject({
    enabled: false,
    interval_minutes: 15,
    question_ids: ['q'],
  })
})
it('rejects enabled groups with no selected question', async () => {
  const save = vi.fn()
  render(<ChannelMonitorConfigForm data={data} onSave={save} pending={false} />)
  await userEvent.click(screen.getByRole('checkbox', { name: 'Question' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByRole('alert')).toBeVisible()
  expect(save).not.toHaveBeenCalled()
})

it('adds a custom question and includes it in the selected group', async () => {
  const save = vi.fn()
  render(<ChannelMonitorConfigForm data={data} pending={false} onSave={save} />)
  await userEvent.click(screen.getByRole('button', { name: 'Add question' }))
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Question name' }),
    'Drawing'
  )
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Prompt' }),
    'Return an SVG animation'
  )
  await userEvent.click(screen.getByRole('checkbox', { name: 'Drawing' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(save).toHaveBeenCalled())
  const config = save.mock.calls[0][0]
  expect(config.questions[0]).toMatchObject({
    name: 'Drawing',
    artifact_type: 'html',
    matcher: 'none',
  })
  expect(config.groups[0].question_ids).toContain(config.questions[0].id)
})

it('prevents selection changes while a save is pending', async () => {
  render(<ChannelMonitorConfigForm data={data} pending onSave={vi.fn()} />)
  for (const checkbox of screen.getAllByRole('checkbox')) {
    fireEvent.click(checkbox)
    expect(checkbox).toBeChecked()
    expect(checkbox).toHaveAttribute('aria-disabled', 'true')
  }
  expect(screen.getByRole('button', { name: 'Saving...' })).toBeDisabled()
})

it.each([
  { field: 'result type', index: 0, option: 'Text', value: 'HTML / SVG' },
  {
    field: 'validation rule',
    index: 1,
    option: 'Contains',
    value: 'Preview only',
  },
])(
  'prevents an open $field selector changing while a save becomes pending',
  async (testCase) => {
    const customData: ChannelMonitorConfigData = {
      ...data,
      config: {
        ...data.config,
        questions: [
          {
            id: 'custom',
            name: 'Drawing',
            prompt: 'Return SVG',
            artifact_type: 'html',
            matcher: 'none',
            expected: '',
          },
        ],
      },
    }
    const save = vi.fn()
    const view = render(
      <ChannelMonitorConfigForm
        data={customData}
        pending={false}
        onSave={save}
      />
    )
    const selector = screen.getAllByRole('combobox')[testCase.index]
    await userEvent.click(selector)
    expect(screen.getByRole('option', { name: testCase.option })).toBeVisible()
    view.rerender(
      <ChannelMonitorConfigForm data={customData} pending onSave={save} />
    )
    const option = screen.queryByRole('option', { name: testCase.option })
    if (option) await userEvent.click(option)
    expect(selector).toHaveTextContent(testCase.value)
    expect(selector).toBeDisabled()
  }
)
