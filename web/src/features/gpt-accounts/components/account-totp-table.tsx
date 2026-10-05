import type {
  ColumnDef,
  OnChangeFn,
  PaginationState,
} from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestamp } from '@/lib/format'

import type { GptAccount } from '../types'

interface AccountTotpTableProps {
  items: GptAccount[]
  total: number
  pagination: PaginationState
  onPaginationChange: OnChangeFn<PaginationState>
  isLoading: boolean
  isFetching: boolean
  encryptionAvailable: boolean
  onAction: (account: GptAccount, mode: 'save' | 'reveal') => void
}

export function AccountTotpTable(props: AccountTotpTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const onAction = props.onAction
  const columns = useMemo<ColumnDef<GptAccount, unknown>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('Account'),
        cell: ({ row }) => (
          <div className='min-w-0'>
            <p className='truncate font-medium'>{row.original.name}</p>
            <p className='text-muted-foreground truncate text-sm'>
              {row.original.email}
            </p>
          </div>
        ),
      },
      {
        accessorKey: 'channel_id',
        header: t('Channel'),
        cell: ({ row }) =>
          row.original.channel_id
            ? row.original.channel_name || `#${row.original.channel_id}`
            : '-',
      },
      {
        accessorKey: 'has_totp',
        header: t('Saved secret'),
        cell: ({ row }) =>
          row.original.has_totp ? '••••••••' : t('Not configured'),
      },
      {
        accessorKey: 'totp_status',
        header: t('Status'),
        cell: ({ row }) => {
          if (!row.original.has_totp) return t('Not configured')
          const confirmed = row.original.totp_status === 'operator_confirmed'
          return (
            <StatusBadge
              label={confirmed ? t('Operator confirmed') : t('Saved')}
              variant='info'
              copyable={false}
            />
          )
        },
      },
      {
        accessorKey: 'totp_version',
        header: t('Secret version'),
        cell: ({ row }) =>
          row.original.has_totp
            ? formatNumber(row.original.totp_version ?? 0, locale)
            : '-',
      },
      {
        accessorKey: 'totp_updated_at',
        header: t('Secret updated'),
        cell: ({ row }) =>
          row.original.totp_updated_at
            ? formatTimestamp(row.original.totp_updated_at)
            : '-',
      },
      {
        id: 'actions',
        header: t('Actions'),
        enableHiding: false,
        cell: ({ row }) => (
          <div className='flex flex-wrap gap-1'>
            <Button
              variant='outline'
              size='sm'
              disabled={!row.original.can_manage || !props.encryptionAvailable}
              aria-label={t('Save replacement for {{name}}', {
                name: row.original.name,
              })}
              onClick={() => onAction(row.original, 'save')}
            >
              {t('Save replacement')}
            </Button>
            <Button
              variant='ghost'
              size='sm'
              disabled={
                !row.original.can_manage ||
                !row.original.has_totp ||
                !props.encryptionAvailable
              }
              aria-label={t('Reveal secret for {{name}}', {
                name: row.original.name,
              })}
              onClick={() => onAction(row.original, 'reveal')}
            >
              {t('Reveal secret')}
            </Button>
          </div>
        ),
      },
    ],
    [t, locale, props.encryptionAvailable, onAction]
  )
  const { table } = useDataTable({
    data: props.items,
    columns,
    pagination: props.pagination,
    onPaginationChange: props.onPaginationChange,
    totalCount: props.total,
    manualPagination: true,
    manualFiltering: true,
    getRowId: (row) => String(row.id),
  })
  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={props.isLoading}
      isFetching={props.isFetching}
      toolbarProps={null}
      emptyTitle={t('No GPT accounts')}
      emptyDescription={t('Add an account to authorize its Codex channel.')}
    />
  )
}
