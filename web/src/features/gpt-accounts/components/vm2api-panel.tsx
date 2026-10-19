import type { OnChangeFn, PaginationState } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePagination, useDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import type { GptAccount } from '../types'
import { Vm2ApiAccountPanel } from './vm2api-account-panel'

export function Vm2ApiPanel(props: {
  accounts: GptAccount[]
  total: number
  pagination: PaginationState
  onPaginationChange: OnChangeFn<PaginationState>
  queryScope: string
}) {
  const { t } = useTranslation()
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const accounts = props.accounts.filter(
    (account) => account.gateway_provider === 'vm2api' && account.gateway_id
  )
  const selected =
    accounts.find((account) => account.id === selectedId) ?? accounts[0]
  const { table } = useDataTable({
    data: props.accounts,
    columns: [],
    totalCount: props.total,
    manualPagination: true,
    pagination: props.pagination,
    onPaginationChange: props.onPaginationChange,
  })

  return (
    <section className='space-y-4 p-1' aria-label={t('VM2API management')}>
      <p className='text-muted-foreground text-sm'>
        {t('Manage VM2API slots without opening its console.')}
      </p>
      {selected ? (
        <>
          <NativeSelect
            aria-label={t('VM2API account')}
            value={selected.id}
            onChange={(event) => setSelectedId(Number(event.target.value))}
          >
            {accounts.map((account) => (
              <NativeSelectOption key={account.id} value={account.id}>
                {account.name} · {account.email}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <Vm2ApiAccountPanel
            key={`${props.queryScope}:${selected.id}`}
            account={selected}
            queryScope={props.queryScope}
          />
        </>
      ) : (
        <EmptyState
          title={t('No VM2API accounts on this page')}
          description={t(
            'Add an account with VM2API enabled, or check another account page.'
          )}
        />
      )}
      <DataTablePagination table={table} compact />
    </section>
  )
}
