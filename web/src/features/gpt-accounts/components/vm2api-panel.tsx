import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'

import {
  getGptAccountVm2ApiLogs,
  getGptAccountVm2ApiStatus,
  refreshGptAccountVm2ApiQuota,
  setGptAccountVm2ApiSchedulable,
} from '../api'
import type { GptAccount } from '../types'

function valueOf(vm: Record<string, unknown>, ...keys: string[]) {
  for (const key of keys) {
    const value = vm[key]
    if (value !== undefined && value !== null && value !== '') return String(value)
  }
  return '-'
}

export function Vm2ApiPanel(props: { accounts: GptAccount[] }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const accounts = props.accounts.filter(
    (account) => account.gateway_provider === 'vm2api' && account.gateway_id
  )
  const [selectedId, setSelectedId] = useState<number | null>(null)
  useEffect(() => {
    if (!accounts.some((account) => account.id === selectedId)) {
      setSelectedId(accounts[0]?.id ?? null)
    }
  }, [accounts, selectedId])
  const selected = accounts.find((account) => account.id === selectedId)
  const status = useQuery({
    queryKey: ['gpt-account-vm2api', selectedId],
    queryFn: ({ signal }) => {
      if (selectedId === null) throw new Error('account_not_selected')
      return getGptAccountVm2ApiStatus(selectedId, signal)
    },
    enabled: selectedId !== null,
    refetchInterval: 15000,
  })
  const logs = useQuery({
    queryKey: ['gpt-account-vm2api-logs', selectedId],
    queryFn: ({ signal }) => {
      if (selectedId === null) throw new Error('account_not_selected')
      return getGptAccountVm2ApiLogs(selectedId, signal)
    },
    enabled: selectedId !== null,
    refetchInterval: 30000,
  })
  const schedulable = useMutation({
    mutationFn: (enabled: boolean) => {
      if (selectedId === null) throw new Error('account_not_selected')
      return setGptAccountVm2ApiSchedulable(selectedId, enabled)
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['gpt-account-vm2api', selectedId] })
    },
  })
  const quota = useMutation({
    mutationFn: () => {
      if (selectedId === null) throw new Error('account_not_selected')
      return refreshGptAccountVm2ApiQuota(selectedId)
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['gpt-account-vm2api', selectedId] })
    },
  })

  if (!accounts.length) return null
  if (status.isLoading) return <LoadingState />
  if (status.isError || !status.data) return <ErrorState onRetry={() => void status.refetch()} />

  const vm = status.data.vm
  const quotaData = status.data.quota ?? (vm.quota as Record<string, unknown> | undefined) ?? {}
  const logItems = logs.data?.items ?? []
  const isSchedulable = vm.schedulable !== false

  return (
    <section className='space-y-3 rounded-lg border p-3' aria-label={t('VM2API management')}>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h2 className='font-medium'>{t('VM2API management')}</h2>
          <p className='text-muted-foreground text-sm'>{t('Manage VM2API slots without opening its console.')}</p>
        </div>
        <div className='flex flex-wrap gap-2'>
          {accounts.map((account) => (
            <Button
              key={account.id}
              variant={account.id === selectedId ? 'default' : 'outline'}
              size='sm'
              onClick={() => setSelectedId(account.id)}
            >
              {account.name}
            </Button>
          ))}
        </div>
      </div>
      <StaticDataTable
        data={[
          { key: t('Slot'), value: status.data.vm_id },
          { key: t('Email'), value: valueOf(vm, 'email') },
          { key: t('Schedule'), value: isSchedulable ? t('Enabled') : t('Disabled') },
          { key: t('Concurrency'), value: valueOf(vm, 'max_concurrency', 'maxConcurrency') },
          { key: t('5-hour quota'), value: valueOf(quotaData, 'utilization_5h') },
          { key: t('7-day quota'), value: valueOf(quotaData, 'utilization_7d') },
          { key: t('5-hour reset'), value: valueOf(quotaData, 'reset_5h') },
          { key: t('7-day reset'), value: valueOf(quotaData, 'reset_7d') },
        ]}
        getRowKey={(row) => row.key}
        columns={[
          { id: 'key', header: t('Metric'), cell: (row) => row.key },
          { id: 'value', header: t('Value'), cell: (row) => row.value },
        ]}
      />
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          size='sm'
          disabled={schedulable.isPending}
          onClick={() => schedulable.mutate(!isSchedulable)}
        >
          {isSchedulable ? t('Pause scheduling') : t('Resume scheduling')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={quota.isPending}
          onClick={() => quota.mutate()}
        >
          <RefreshCw />
          {t('Refresh quota')}
        </Button>
      </div>
      <div>
        <h3 className='mb-2 font-medium'>{t('VM2API request logs')}</h3>
        {logs.isError ? (
          <p className='text-destructive text-sm'>{t('Request logs could not be loaded.')}</p>
        ) : (
          <StaticDataTable
            data={logItems}
            getRowKey={(row, index) => String(row.id ?? row.request_id ?? index)}
            emptyContent={t('No request logs')}
            columns={[
              { id: 'time', header: t('Time'), cell: (row) => valueOf(row, 'created_at', 'timestamp', 'time') },
              { id: 'model', header: t('Model'), cell: (row) => valueOf(row, 'model') },
              { id: 'status', header: t('Status'), cell: (row) => valueOf(row, 'status', 'status_code') },
              { id: 'error', header: t('Error'), cell: (row) => valueOf(row, 'error', 'error_class') },
            ]}
          />
        )}
      </div>
      {selected && <span className='sr-only'>{selected.email}</span>}
    </section>
  )
}
