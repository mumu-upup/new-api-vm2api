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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { MarketplaceSettingsSection } from '../marketplace-settings-section'

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useBlocker: () => ({ status: 'idle' }),
  Link: (props: { children: React.ReactNode }) => <span>{props.children}</span>,
}))

function Fixture(props: { catalog: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <MarketplaceSettingsSection
          catalog={props.catalog}
          groupRatio='{"default":1}'
        />
      </SettingsPageProvider>
    </>
  )
}
function renderEditor(catalog: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <Fixture catalog={catalog} />
    </QueryClientProvider>
  )
  return client
}
beforeEach(() => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { items: [{ id: 8, name: 'xAI' }], total: 1 },
    },
  })
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})
test('invalid saved JSON presents an error without a save action', async () => {
  renderEditor('{broken')
  expect(
    await screen.findByText(
      'The saved marketplace catalog or billing groups are invalid. Correct the saved configuration before editing.'
    )
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Save Changes' })
  ).not.toBeInTheDocument()
})
test('a stale vendor remains identifiable and blocks saving until removed', async () => {
  renderEditor(
    '{"version":1,"vendors":[{"vendor_id":99,"region":"domestic","groups":["default"]}]}'
  )
  expect(await screen.findByText('Missing vendor ID: 99')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Remove vendor' }))
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'marketplace_setting.catalog',
      value: '{"version":1,"vendors":[]}',
    })
  )
})

test('a saved vendor beyond the first server page remains editable without losing its association', async () => {
  vi.mocked(api.get).mockImplementation(async (_url, config) => {
    const page = config?.params?.p === 2 ? 2 : 1
    const items =
      page === 1
        ? Array.from({ length: 100 }, (_, index) => ({
            id: index + 1,
            name: `Vendor ${index + 1}`,
          }))
        : [{ id: 101, name: 'Last vendor' }]
    return {
      data: {
        success: true,
        data: { items, total: 101, page, page_size: 100 },
      },
    }
  })
  renderEditor(
    '{"version":1,"vendors":[{"vendor_id":1,"region":"domestic","groups":["default"]},{"vendor_id":101,"region":"international","groups":["default"]}]}'
  )
  expect(
    await screen.findByRole('button', { name: 'Save Changes' })
  ).toBeEnabled()
  expect(screen.getAllByRole('combobox', { name: 'Vendor' })[1]).toHaveValue(
    'Last vendor'
  )
  expect(screen.queryByText('Missing vendor ID: 101')).not.toBeInTheDocument()
  await userEvent.click(
    screen.getAllByRole('button', { name: 'Remove vendor' })[0]
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'marketplace_setting.catalog',
      value:
        '{"version":1,"vendors":[{"vendor_id":101,"region":"international","groups":["default"]}]}',
    })
  )
})
