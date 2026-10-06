import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import { getAccountErrorMessage } from '../lib/account-error'

/** Keep account credentials confined to a cancellable operation. */
export function useAccountOperation() {
  const { t } = useTranslation()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const role = useAuthStore((state) => state.auth.user?.role)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  const controller = useRef<AbortController | null>(null)
  const activeAction = useRef<(() => Promise<void>) | null>(null)
  const [error, setError] = useState<Pick<
    AuthOperationError,
    'code' | 'message'
  > | null>(null)
  const mutation = useMutation({
    // Do not retain credential closures or results in the cache.
    mutationFn: async () => {
      try {
        await activeAction.current?.()
      } catch (error) {
        const failure = AuthOperationError.from(error)
        throw new AuthOperationError(failure.message, failure.code)
      }
    },
    retry: false,
    gcTime: 0,
    onError: () => undefined,
  })

  useEffect(
    () => () => {
      controller.current?.abort()
      controller.current = null
      activeAction.current = null
    },
    [userId, role, sessionId]
  )

  const execute = async <T>(
    action: (signal: AbortSignal) => Promise<T>
  ): Promise<T | undefined> => {
    if (controller.current) return undefined
    const current = new AbortController()
    controller.current = current
    setError(null)
    try {
      let result: T | undefined
      activeAction.current = async () => {
        current.signal.throwIfAborted()
        result = await action(current.signal)
        current.signal.throwIfAborted()
      }
      await mutation.mutateAsync()
      return result
    } catch (error) {
      if (!current.signal.aborted) {
        const failure = AuthOperationError.from(error)
        setError({ code: failure.code, message: failure.message })
      }
      return undefined
    } finally {
      if (controller.current === current) {
        controller.current = null
        activeAction.current = null
        mutation.reset()
      }
    }
  }

  return {
    execute,
    pending: mutation.isPending,
    error: error
      ? getAccountErrorMessage(error.code ?? error.message, t, error.message)
      : '',
  }
}
