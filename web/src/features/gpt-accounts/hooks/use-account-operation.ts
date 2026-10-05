import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  useSecureVerification,
  type RequestVerificationOptions,
} from '@/features/auth/secure-verification'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import { getAccountErrorMessage } from '../lib/account-error'

/** Keep one-use proofs and account credentials confined to a cancellable operation. */
export function useAccountOperation() {
  const { t } = useTranslation()
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const userId = useAuthStore((state) => state.auth.user?.id)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  const controller = useRef<AbortController | null>(null)
  const [error, setError] = useState<Pick<
    AuthOperationError,
    'code' | 'message'
  > | null>(null)
  const mutation = useMutation({
    mutationFn: (action: () => Promise<unknown>) => action(),
    retry: false,
    gcTime: 0,
    onError: () => undefined,
  })

  useEffect(
    () => () => {
      controller.current?.abort()
      controller.current = null
      cancelVerification()
    },
    [cancelVerification, userId, sessionId]
  )

  const execute = async <T>(
    request: RequestVerificationOptions | null,
    action: (proof: string, signal: AbortSignal) => Promise<T>
  ): Promise<T | undefined> => {
    if (controller.current) return undefined
    const current = new AbortController()
    controller.current = current
    setError(null)
    try {
      return (await mutation.mutateAsync(async () => {
        const proof = request
          ? await verification.requestVerification(request)
          : null
        current.signal.throwIfAborted()
        if (request && !proof) return undefined
        const result = await action(proof?.proof_token ?? '', current.signal)
        current.signal.throwIfAborted()
        return result
      })) as T | undefined
    } catch (error) {
      if (!current.signal.aborted) {
        const failure = AuthOperationError.from(error)
        setError({ code: failure.code, message: failure.message })
      }
      return undefined
    } finally {
      if (controller.current === current) {
        controller.current = null
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
    verification,
  }
}
