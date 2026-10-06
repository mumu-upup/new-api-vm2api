import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FileJson, Plus, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  deleteGptAccount,
  getGptAccountConfig,
  listGptAccounts,
  listGptAccountGateways,
  runGptAccountAction,
} from './api'
import { AccountFormDialog } from './components/account-form-dialog'
import { AccountLoginDialog } from './components/account-login-dialog'
import { AccountTotpDialog } from './components/account-totp-dialog'
import { AccountTotpTable } from './components/account-totp-table'
import { AccountsTable, type AccountAction } from './components/accounts-table'
import { GatewayFormDialog } from './components/gateway-form-dialog'
import { GatewaysPanel } from './components/gateways-panel'
import { OAuthImportDialog } from './components/oauth-import-dialog'
import { useAccountOperation } from './hooks/use-account-operation'
import type { GptAccount, GptAccountGateway } from './types'

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
  const [totp, setTotp] = useState<{
    account: GptAccount
    mode: 'save' | 'reveal'
  } | null>(null)
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [editor, setEditor] = useState<GptAccount | 'create' | null>(null)
  const [login, setLogin] = useState<GptAccount | null>(null)
  const [gatewayEditor, setGatewayEditor] = useState<
    GptAccountGateway | 'create' | null
  >(null)
  const [oauthImportOpen, setOAuthImportOpen] = useState(false)
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
  const gateways = useQuery({
    queryKey: [
      'gpt-account-gateways',
      props.userId,
      props.role,
      props.sessionId,
    ],
    queryFn: ({ signal }) => listGptAccountGateways(signal),
    gcTime: 0,
  })
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ['gpt-accounts'] })
    void client.invalidateQueries({ queryKey: ['gpt-account-gateways'] })
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
    if (kind === 'totp') {
      if (
        account.status !== 'logging_in' &&
        config.data?.encryption_available
      ) {
        setTotp({ account, mode: 'save' })
      }
      return
    }
    const result = await operation.execute(
      {
        scope:
          kind === 'sync' ? 'gpt_account.gateway.sync' : `gpt_account.${kind}`,
        context: { account_id: account.id },
      },
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
          <Button
            variant='outline'
            onClick={() => setOAuthImportOpen(true)}
            disabled={!config.data?.encryption_available}
          >
            <FileJson />
            {t('Import OAuth JSON')}
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
              <Tabs
                defaultValue='accounts'
                className='min-h-0 flex-1'
                onValueChange={() => setTotp(null)}
              >
                <TabsList aria-label={t('GPT Accounts')}>
                  <TabsTrigger value='accounts'>{t('Accounts')}</TabsTrigger>
                  <TabsTrigger value='totp'>{t('2FA management')}</TabsTrigger>
                  {props.role === ROLE.SUPER_ADMIN && (
                    <TabsTrigger value='gateways'>{t('Gateways')}</TabsTrigger>
                  )}
                </TabsList>
                <TabsContent value='accounts' className='min-h-0'>
                  <AccountsTable
                    items={query.data?.items ?? []}
                    total={query.data?.total ?? 0}
                    pagination={pagination}
                    onPaginationChange={setPagination}
                    isLoading={query.isLoading}
                    isFetching={query.isFetching}
                    pending={operation.pending}
                    encryptionAvailable={Boolean(
                      config.data?.encryption_available
                    )}
                    onAction={(account, kind) => {
                      void action(account, kind)
                    }}
                  />
                </TabsContent>
                <TabsContent
                  value='totp'
                  className='flex min-h-0 flex-col gap-3'
                >
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Complete authenticator binding on the official OpenAI website before saving. This gateway does not verify upstream binding.'
                    )}
                  </p>
                  <AccountTotpTable
                    items={query.data?.items ?? []}
                    total={query.data?.total ?? 0}
                    pagination={pagination}
                    onPaginationChange={setPagination}
                    isLoading={query.isLoading}
                    isFetching={query.isFetching}
                    encryptionAvailable={Boolean(
                      config.data?.encryption_available
                    )}
                    onAction={(account, mode) => {
                      if (account.can_manage) setTotp({ account, mode })
                    }}
                  />
                </TabsContent>
                {props.role === ROLE.SUPER_ADMIN && (
                  <TabsContent
                    value='gateways'
                    className='min-h-0 overflow-auto'
                  >
                    <GatewaysPanel
                      items={gateways.data?.items ?? []}
                      isLoading={gateways.isLoading}
                      isError={gateways.isError}
                      encryptionAvailable={Boolean(
                        config.data?.encryption_available
                      )}
                      onRetry={() => {
                        void gateways.refetch()
                      }}
                      onEdit={setGatewayEditor}
                    />
                  </TabsContent>
                )}
              </Tabs>
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {gatewayEditor && (
        <GatewayFormDialog
          gateway={gatewayEditor === 'create' ? undefined : gatewayEditor}
          onClose={() => setGatewayEditor(null)}
          onSaved={refresh}
        />
      )}
      {totp && (
        <AccountTotpDialog
          account={totp.account}
          mode={totp.mode}
          onClose={() => setTotp(null)}
          onSaved={refresh}
        />
      )}
      {editor && (
        <AccountFormDialog
          account={editor === 'create' ? undefined : editor}
          onClose={() => setEditor(null)}
          onSaved={refresh}
          gateways={gateways.data?.items ?? []}
          gatewaysLoading={gateways.isLoading}
          gatewaysError={gateways.isError}
          onRetryGateways={() => {
            void gateways.refetch()
          }}
        />
      )}
      {login && (
        <AccountLoginDialog
          account={login}
          onClose={() => setLogin(null)}
          onSaved={refresh}
        />
      )}
      {oauthImportOpen && (
        <OAuthImportDialog
          onClose={() => setOAuthImportOpen(false)}
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
