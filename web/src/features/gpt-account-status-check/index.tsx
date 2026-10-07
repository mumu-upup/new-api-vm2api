import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { useAccountOperation } from '@/features/gpt-accounts/hooks/use-account-operation'
import { ROLE } from '@/lib/roles'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import {
  cancelStatusCheck,
  createStatusCheck,
  getStatusCheck,
  type ModelTestOptions,
  type StatusCheckView,
} from './api'
import { StatusCheckForm } from './components/status-check-form'
import { StatusCheckResults } from './components/status-check-results'

export function GptAccountStatusCheck() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const sessionId = useAuthStore((state) => state.auth.session?.sid ?? '')

  if (!user || user.role < ROLE.ADMIN) {
    return <ErrorState title={t('Access denied')} />
  }

  return (
    <GptAccountStatusCheckContent
      key={`${user.id}:${user.role}:${sessionId}`}
      role={user.role}
      sessionId={sessionId}
      userId={user.id}
    />
  )
}

type GptAccountStatusCheckContentProps = {
  userId: number
  role: number
  sessionId: string
}

function GptAccountStatusCheckContent(
  props: GptAccountStatusCheckContentProps
) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const operation = useAccountOperation()
  const jobStorageKey = `gpt-account-status-check:${props.userId}:${props.sessionId}`
  const [jobId, setJobId] = useState<string | null>(() => {
    try {
      const stored = sessionStorage.getItem(jobStorageKey)
      return stored && stored.length <= 128 ? stored : null
    } catch {
      return null
    }
  })

  const queryKey = [
    'gpt-account-status-check',
    props.userId,
    props.role,
    props.sessionId,
    jobId,
  ] as const
  const query = useQuery({
    queryKey,
    enabled: jobId != null && !operation.pending,
    queryFn: ({ signal }) => getStatusCheck(jobId ?? '', signal),
    refetchInterval: (current) => {
      const state = current.state.data?.state
      return !current.state.error &&
        !operation.pending &&
        (state === 'pending' || state === 'running')
        ? 1000
        : false
    },
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })

  const currentView = query.data ?? null
  const expired =
    query.error instanceof AuthOperationError &&
    query.error.code === 'status_check_not_found'
  const active =
    !expired &&
    (currentView?.state === 'pending' || currentView?.state === 'running')

  const submit = async (credentials: string, modelTest?: ModelTestOptions) => {
    const created = await operation.execute((signal) =>
      createStatusCheck(credentials, signal, modelTest)
    )
    if (!created) return
    const createdKey = [
      'gpt-account-status-check',
      props.userId,
      props.role,
      props.sessionId,
      created.job_id,
    ] as const
    queryClient.setQueryData<StatusCheckView>(createdKey, created)
    // Retain only the opaque job ID for refresh/navigation recovery in this tab.
    // Credentials and results never enter browser storage.
    try {
      sessionStorage.setItem(jobStorageKey, created.job_id)
    } catch {
      /* Storage may be disabled. */
    }
    setJobId(created.job_id)
  }

  const cancel = async () => {
    if (!jobId || !currentView) return
    await queryClient.cancelQueries({ queryKey })
    const cancelled = await operation.execute((signal) =>
      cancelStatusCheck(jobId, signal)
    )
    if (cancelled) {
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData<StatusCheckView>(queryKey, cancelled)
    }
  }

  let results = (
    <EmptyState
      className='lg:min-h-0'
      description={t(
        'Completed checks appear here and are cleared from memory after a short time.'
      )}
      title={t('No checks yet')}
    />
  )
  if (expired) {
    results = (
      <ErrorState
        title={t('Expired')}
        description={t('This check has expired. Start a new check.')}
      />
    )
  } else if (currentView) {
    results = (
      <StatusCheckResults
        cancelling={operation.pending}
        error={query.isError}
        onCancel={cancel}
        onRetry={() => void query.refetch()}
        view={currentView}
      />
    )
  } else if (jobId && query.isError) {
    results = (
      <ErrorState
        title={t('Status check unavailable')}
        onRetry={() => void query.refetch()}
      />
    )
  } else if (jobId && query.isPending) {
    results = <LoadingState />
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('GPT Account Status Check')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='grid items-start gap-4 xl:grid-cols-[minmax(20rem,2fr)_minmax(0,3fr)]'>
          <StatusCheckForm
            disabled={operation.pending || active}
            onSubmit={submit}
          />
          {operation.error && (
            <p role='alert' className='text-destructive text-sm xl:col-span-2'>
              {operation.error}
            </p>
          )}
          {results}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
