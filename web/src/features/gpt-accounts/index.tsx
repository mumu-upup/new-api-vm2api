import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  deleteGptAccount,
  getGptAccountConfig,
  listGptAccounts,
  runGptAccountAction,
} from './api'
import { AccountFormDialog } from './components/account-form-dialog'
import { AccountLoginDialog } from './components/account-login-dialog'
import { AccountsTable, type AccountAction } from './components/accounts-table'
import { useAccountOperation } from './hooks/use-account-operation'
import type { GptAccount } from './types'

export function GptAccounts() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  if (!user || user.role < ROLE.ADMIN) {
    return <ErrorState title={t('Access denied')} />
  }
  return (
    <GptAccountsPage
      key={`${user.id}:${user.role}:${sessionId ?? ''}`}
      userId={user.id}
      role={user.role}
      sessionId={sessionId}
    />
  )
}

function GptAccountsPage(props: {
  userId: number
  role: number
  sessionId?: string
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const operation = useAccountOperation()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [editor, setEditor] = useState<GptAccount | 'create' | null>(null)
  const [login, setLogin] = useState<GptAccount | null>(null)
  const [deleting, setDeleting] = useState<GptAccount | null>(null)
  const query = useQuery({
    queryKey: [
      'gpt-accounts',
      props.userId,
      props.role,
      props.sessionId,
      'list',
      pagination,
    ],
    gcTime: 0,
    queryFn: ({ signal }) =>
      listGptAccounts(pagination.pageIndex + 1, pagination.pageSize, signal),
    refetchInterval: (current) =>
      current.state.status !== 'error' &&
      current.state.data?.items.some((item) => item.status === 'logging_in')
        ? 3000
        : false,
    refetchIntervalInBackground: false,
  })
  const config = useQuery({
    queryKey: [
      'gpt-accounts',
      props.userId,
      props.role,
      props.sessionId,
      'config',
    ],
    gcTime: 0,
    queryFn: ({ signal }) => getGptAccountConfig(signal),
  })
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ['gpt-accounts'] })
    void client.invalidateQueries({ queryKey: ['channels'] })
  }
  const action = async (account: GptAccount, kind: AccountAction) => {
    if (!account.can_manage) return
    if (kind === 'edit') {
      setEditor(account)
      return
    }
    if (kind === 'oauth') {
      setLogin(account)
      return
    }
    if (kind === 'delete') {
      setDeleting(account)
      return
    }
    const result = await operation.execute(
      { scope: `gpt_account.${kind}`, context: { account_id: account.id } },
      (proof, signal) => runGptAccountAction(account.id, kind, proof, signal)
    )
    if (result) refresh()
  }
  const remove = async () => {
    if (!deleting) return
    const result = await operation.execute(
      { scope: 'gpt_account.delete', context: { account_id: deleting.id } },
      async (proof, signal) => {
        await deleteGptAccount(deleting.id, proof, signal)
        return true
      }
    )
    if (!result) return
    setDeleting(null)
    refresh()
  }
  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('GPT Accounts')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button
            variant='outline'
            size='icon'
            onClick={refresh}
            disabled={query.isFetching}
            aria-label={t('Refresh')}
          >
            <RefreshCw />
          </Button>
          <Button
            onClick={() => setEditor('create')}
            disabled={!config.data?.encryption_available}
          >
            <Plus />
            {t('Add GPT account')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-3'>
            {config.isError && (
              <p role='alert' className='text-destructive text-sm'>
                {t(
                  'Account configuration could not be loaded. Refresh to retry.'
                )}
              </p>
            )}
            {config.data && !config.data.encryption_available && (
              <p role='alert' className='text-destructive text-sm'>
                {t(
                  'Credential encryption is not configured. Account creation is unavailable.'
                )}
              </p>
            )}
            {config.data && !config.data.automation_available && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Automatic sign-in is unavailable. Use browser sign-in to authorize an account.'
                )}
              </p>
            )}
            {operation.error && (
              <p role='alert' className='text-destructive text-sm'>
                {t(operation.error)}
              </p>
            )}
            {query.isError ? (
              <ErrorState onRetry={refresh} />
            ) : (
              <AccountsTable
                items={query.data?.items ?? []}
                total={query.data?.total ?? 0}
                pagination={pagination}
                onPaginationChange={setPagination}
                isLoading={query.isLoading}
                isFetching={query.isFetching}
                pending={operation.pending}
                onAction={(account, kind) => {
                  void action(account, kind)
                }}
              />
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {editor && (
        <AccountFormDialog
          account={editor === 'create' ? undefined : editor}
          onClose={() => setEditor(null)}
          onSaved={refresh}
        />
      )}
      {login && (
        <AccountLoginDialog
          account={login}
          onClose={() => setLogin(null)}
          onSaved={refresh}
        />
      )}
      <ConfirmDialog
        open={Boolean(deleting) && !operation.verification.isActive}
        onOpenChange={(open) => {
          if (!open && !operation.pending) setDeleting(null)
        }}
        title={t('Delete GPT account?')}
        desc={t(
          'This deletes the stored account credentials. The associated channel is kept.'
        )}
        destructive
        isLoading={operation.pending}
        confirmText={t('Delete')}
        handleConfirm={() => {
          void remove()
        }}
      >
        {operation.error && (
          <p role='alert' className='text-destructive text-sm'>
            {operation.error}
          </p>
        )}
      </ConfirmDialog>
      <SecureVerificationDialog {...operation.verification.dialogProps} />
    </>
  )
}
