import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestamp } from '@/lib/format'

import {
  getGptAccountVm2ApiInstance,
  getGptAccountVm2ApiLogs,
  getGptAccountVm2ApiStatus,
  refreshGptAccountVm2ApiQuota,
  runGptAccountVm2ApiInstanceAction,
  setGptAccountVm2ApiSchedulable,
} from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import { getAccountErrorMessage } from '../lib/account-error'
import type { GptAccount, Vm2ApiInstanceAction } from '../types'

function valueOf(record: Record<string, unknown>, ...keys: string[]) {
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string' || typeof value === 'number') {
      return String(value)
    }
  }
  return '-'
}

function seconds(value: unknown, locale: string | undefined) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
    return '-'
  }
  return `${formatNumber(value / 1000, locale)} s`
}

export function Vm2ApiAccountPanel(props: {
  account: GptAccount
  queryScope: string
  pending: boolean
  onDelete: (account: GptAccount) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const operation = useAccountOperation()
  const [confirm, setConfirm] = useState<Vm2ApiInstanceAction | null>(null)
  const id = props.account.id
  const queryKey = ['gpt-account-vm2api', props.queryScope, id]
  const instance = useQuery({
    queryKey: [...queryKey, 'instance'],
    queryFn: ({ signal }) => getGptAccountVm2ApiInstance(id, signal),
    gcTime: 0,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  })
  const status = useQuery({
    queryKey: [...queryKey, 'status'],
    queryFn: ({ signal }) => getGptAccountVm2ApiStatus(id, signal),
    gcTime: 0,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  })
  const logs = useQuery({
    queryKey: [...queryKey, 'logs'],
    queryFn: ({ signal }) => getGptAccountVm2ApiLogs(id, signal),
    gcTime: 0,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
  const refresh = () => {
    void client.invalidateQueries({ queryKey })
    void client.invalidateQueries({ queryKey: ['gpt-accounts'] })
  }
  const lifecycle = async (action: Vm2ApiInstanceAction) => {
    if (!props.account.can_manage) return
    const result = await operation.execute((signal) =>
      runGptAccountVm2ApiInstanceAction(id, action, signal)
    )
    if (result) {
      setConfirm(null)
      refresh()
    }
  }
  const updateSlot = async (action: 'schedule' | 'quota') => {
    if (!props.account.can_manage || !status.data) return
    const result = await operation.execute(() =>
      action === 'quota'
        ? refreshGptAccountVm2ApiQuota(id)
        : setGptAccountVm2ApiSchedulable(
            id,
            status.data.vm.schedulable === false
          )
    )
    if (result) refresh()
  }
  const vm = status.data?.vm ?? {}
  const quota =
    status.data?.quota ??
    (vm.quota as Record<string, unknown> | undefined) ??
    {}
  const schedulingKnown = typeof vm.schedulable === 'boolean'
  const instanceData = instance.data
  const dedicated = instanceData?.isolation === 'dedicated'
  const stateLabels: Record<string, string> = {
    shared: t('Shared gateway'),
    ready: t('Ready'),
    running: t('Running'),
    stopped: t('Stopped'),
    missing: t('Not found'),
    pending: t('Pending'),
    syncing: t('Synchronizing'),
    unavailable: t('Unavailable'),
    provisioning: t('Initializing'),
    migrating: t('Migrating'),
    error: t('Error'),
    failed: t('Failed'),
    pending_cleanup: t('Pending cleanup'),
  }
  const metrics = [
    {
      key: t('Isolation'),
      value: dedicated
        ? t('Dedicated container and data')
        : t('Shared gateway'),
    },
    {
      key: t('Status'),
      value:
        stateLabels[instanceData?.status ?? ''] ?? instanceData?.status ?? '-',
    },
    { key: t('Container'), value: instanceData?.container_name ?? '-' },
    {
      key: t('Runtime status'),
      value:
        stateLabels[instanceData?.runtime_status ?? ''] ??
        instanceData?.runtime_status ??
        '-',
    },
    { key: t('Image'), value: instanceData?.image ?? '-' },
    {
      key: t('Last updated'),
      value: instanceData?.updated_at
        ? formatTimestamp(instanceData.updated_at)
        : '-',
    },
    {
      key: t('Error'),
      value: instanceData?.error_code
        ? getAccountErrorMessage(instanceData.error_code, t)
        : '-',
    },
  ]
  const columns = [
    {
      id: 'key',
      header: t('Metric'),
      cell: (row: { key: string; value: string }) => row.key,
    },
    {
      id: 'value',
      header: t('Value'),
      cell: (row: { key: string; value: string }) => (
        <span className='break-all'>{row.value}</span>
      ),
    },
  ]
  const actionLabels: Record<Vm2ApiInstanceAction, string> = {
    start: t('Start instance'),
    stop: t('Stop instance'),
    restart: t('Restart instance'),
    rebuild: t('Rebuild instance'),
    provision: t('Create dedicated instance'),
  }
  return (
    <div className='space-y-4'>
      {props.account.can_manage && (
        <Button
          variant='destructive'
          aria-label={t('Delete {{name}}', { name: props.account.name })}
          disabled={props.pending || operation.pending}
          onClick={() => props.onDelete(props.account)}
        >
          {t('Delete')}
        </Button>
      )}
      {instance.isLoading && <LoadingState />}
      {instance.isError && (
        <ErrorState
          title={t('Instance status could not be loaded.')}
          onRetry={() => void instance.refetch()}
        />
      )}
      {instanceData && (
        <StaticDataTable
          data={metrics}
          getRowKey={(row) => row.key}
          columns={columns}
        />
      )}
      {props.account.can_manage && instanceData && (
        <div className='flex flex-wrap gap-2'>
          {dedicated ? (
            (['start', 'stop', 'restart', 'rebuild'] as const).map((action) => (
              <Button
                key={action}
                variant='outline'
                disabled={operation.pending}
                onClick={() => setConfirm(action)}
              >
                {actionLabels[action]}
              </Button>
            ))
          ) : (
            <Button
              disabled={operation.pending}
              onClick={() => setConfirm('provision')}
            >
              {actionLabels.provision}
            </Button>
          )}
          {dedicated && (
            <Button
              variant='outline'
              disabled={operation.pending}
              onClick={() => void lifecycle('provision')}
            >
              {t('Retry synchronization')}
            </Button>
          )}
        </div>
      )}
      {operation.error && (
        <p role='alert' className='text-destructive text-sm'>
          {operation.error}
        </p>
      )}
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open && !operation.pending) setConfirm(null)
        }}
        title={confirm ? actionLabels[confirm] : ''}
        desc={t(
          'This changes the VM2API instance for {{name}} and may briefly interrupt its requests. Account data is preserved.',
          { name: props.account.name }
        )}
        isLoading={operation.pending}
        handleConfirm={() => {
          if (confirm) void lifecycle(confirm)
        }}
      />
      {status.isLoading && <LoadingState />}
      {status.isError && (
        <ErrorState
          title={t(
            'VM2API is unavailable. Instance controls remain accessible.'
          )}
          onRetry={() => void status.refetch()}
        />
      )}
      {status.data && (
        <>
          <StaticDataTable
            data={[
              { key: t('Slot'), value: status.data.vm_id },
              { key: t('Email'), value: valueOf(vm, 'email') },
              {
                key: t('Schedule'),
                value: schedulingKnown
                  ? t(vm.schedulable ? 'Enabled' : 'Disabled')
                  : '-',
              },
              {
                key: t('Concurrency'),
                value: valueOf(vm, 'max_concurrency', 'maxConcurrency'),
              },
              {
                key: t('5-hour quota'),
                value:
                  typeof quota.utilization_5h === 'number'
                    ? `${formatNumber(quota.utilization_5h * 100, locale)}%`
                    : '-',
              },
              {
                key: t('7-day quota'),
                value:
                  typeof quota.utilization_7d === 'number'
                    ? `${formatNumber(quota.utilization_7d * 100, locale)}%`
                    : '-',
              },
              { key: t('5-hour reset'), value: valueOf(quota, 'reset_5h') },
              { key: t('7-day reset'), value: valueOf(quota, 'reset_7d') },
            ]}
            getRowKey={(row) => row.key}
            columns={columns}
          />
          {props.account.can_manage && (
            <div className='flex flex-wrap gap-2'>
              {schedulingKnown && (
                <Button
                  variant='outline'
                  disabled={operation.pending}
                  onClick={() => void updateSlot('schedule')}
                >
                  {vm.schedulable === false
                    ? t('Resume scheduling')
                    : t('Pause scheduling')}
                </Button>
              )}
              <Button
                variant='outline'
                disabled={operation.pending}
                onClick={() => void updateSlot('quota')}
              >
                {t('Refresh quota')}
              </Button>
            </div>
          )}
        </>
      )}
      <h3 className='font-medium'>{t('VM2API request logs')}</h3>
      {logs.isLoading && <LoadingState />}
      {logs.isError ? (
        <ErrorState
          title={t('Request logs could not be loaded.')}
          onRetry={() => void logs.refetch()}
        />
      ) : (
        <StaticDataTable
          data={logs.data?.items ?? []}
          getRowKey={(row, index) => String(row.id ?? row.request_id ?? index)}
          emptyContent={t('No request logs')}
          columns={[
            {
              id: 'time',
              header: t('Time'),
              cell: (row) => valueOf(row, 'created_at', 'timestamp', 'time'),
            },
            {
              id: 'model',
              header: t('Model'),
              cell: (row) => valueOf(row, 'model'),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (row) => valueOf(row, 'status', 'status_code'),
            },
            {
              id: 'ttft',
              header: t('Time to first token'),
              cell: (row) => seconds(row.first_token_ms, locale),
            },
            {
              id: 'duration',
              header: t('Total duration'),
              cell: (row) => seconds(row.duration_ms, locale),
            },
            {
              id: 'error',
              header: t('Error'),
              cell: (row) => valueOf(row, 'error_code', 'error_class'),
            },
          ]}
        />
      )}
    </div>
  )
}
