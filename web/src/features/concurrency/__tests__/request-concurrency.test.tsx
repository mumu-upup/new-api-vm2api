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
import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  useTokenConcurrency,
  useUserConcurrency,
} from '../use-request-concurrency'

const clients: QueryClient[] = []

function queryWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return {
    client,
    wrapper: (props: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        {props.children}
      </QueryClientProvider>
    ),
  }
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.useRealTimers()
  focusManager.setFocused(undefined)
})

it('replaces stale counts with unknown on failure and recovers on a successful refresh', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        user_id: 1,
        current: 6,
        max_concurrent_requests: 10,
        tokens: { '2': 3 },
      },
    },
  })
  const { client, wrapper } = queryWrapper()
  const { result } = renderHook(() => useTokenConcurrency([2]), { wrapper })
  await waitFor(() => expect(result.current?.current).toBe(6))
  get.mockResolvedValue({ data: { success: false, message: 'Unavailable' } })
  await act(async () => {
    await client.refetchQueries({ queryKey: ['request-concurrency'] })
  })
  await waitFor(() => expect(result.current).toBeUndefined())
  get.mockResolvedValue({
    data: {
      success: true,
      data: {
        user_id: 1,
        current: 0,
        max_concurrent_requests: 10,
        tokens: { '2': 0 },
      },
    },
  })
  await act(async () => {
    await client.refetchQueries({ queryKey: ['request-concurrency'] })
  })
  await waitFor(() => expect(result.current?.current).toBe(0))
})

it('requests only the current page ids and clears previous-page counts while loading', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { '2': { user_id: 2, current: 3, max_concurrent_requests: 8 } },
    },
  })
  const { wrapper } = queryWrapper()
  const { result, rerender } = renderHook(
    (props: { ids: number[] }) => useUserConcurrency(props.ids),
    { wrapper, initialProps: { ids: [2] } }
  )
  await waitFor(() => expect(result.current?.['2'].current).toBe(3))
  get.mockImplementation(() => new Promise(() => {}))
  rerender({ ids: [5, 6] })
  expect(result.current).toBeUndefined()
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/user/concurrency',
      expect.objectContaining({ params: { ids: '5,6' } })
    )
  )
})

it('does not query an empty admin page but loads the account total without API keys', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        user_id: 1,
        current: 0,
        max_concurrent_requests: 0,
        tokens: {},
      },
    },
  })
  const { wrapper } = queryWrapper()
  renderHook(() => useUserConcurrency([]), { wrapper })
  expect(get).not.toHaveBeenCalled()
  const { result } = renderHook(() => useTokenConcurrency([]), { wrapper })
  await waitFor(() => expect(result.current?.current).toBe(0))
  expect(get).toHaveBeenCalledWith(
    '/api/token/concurrency',
    expect.objectContaining({ params: { ids: '' } })
  )
})

it('polls every three seconds only while the page is visible', async () => {
  vi.useFakeTimers()
  focusManager.setFocused(true)
  let current = 1
  const get = vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: {
      success: true,
      data: {
        user_id: 1,
        current,
        max_concurrent_requests: 10,
        tokens: { '2': current },
      },
    },
  }))
  const { wrapper } = queryWrapper()
  const { result } = renderHook(() => useTokenConcurrency([2]), { wrapper })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1)
  })
  expect(result.current?.current).toBe(1)
  current = 2
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000)
  })
  expect(result.current?.current).toBe(2)
  focusManager.setFocused(false)
  const requests = get.mock.calls.length
  current = 3
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000)
  })
  expect(get).toHaveBeenCalledTimes(requests)
  expect(result.current?.current).toBe(2)
})
