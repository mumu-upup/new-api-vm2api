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
import { useQuery } from '@tanstack/react-query'
import { createContext } from 'react'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

export interface UserConcurrency {
  user_id: number
  current: number
  max_concurrent_requests: number
}

export interface TokenConcurrency extends UserConcurrency {
  tokens: Record<string, number>
}

export const UserConcurrencyContext = createContext<
  Record<string, UserConcurrency> | undefined
>(undefined)
export const TokenConcurrencyContext = createContext<
  TokenConcurrency | undefined
>(undefined)

const pollingOptions = {
  refetchInterval: 3_000,
  refetchIntervalInBackground: false,
  retry: false,
  meta: { errorToast: false },
} as const

export function useTokenConcurrency(ids: number[], enabled = true) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    ...pollingOptions,
    queryKey: ['request-concurrency', 'tokens', userId, ids],
    enabled,
    queryFn: async ({ signal }): Promise<TokenConcurrency> => {
      const response = await api.get('/api/token/concurrency', {
        params: { ids: ids.join(',') },
        signal,
      })
      return requireServerSuccess(response.data).data
    },
  })
  return query.isError ? undefined : query.data
}

export function useUserConcurrency(ids: number[]) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    ...pollingOptions,
    queryKey: ['request-concurrency', 'users', userId, ids],
    enabled: ids.length > 0,
    queryFn: async ({ signal }): Promise<Record<string, UserConcurrency>> => {
      const response = await api.get('/api/user/concurrency', {
        params: { ids: ids.join(',') },
        signal,
      })
      return requireServerSuccess(response.data).data
    },
  })
  return query.isError ? undefined : query.data
}
