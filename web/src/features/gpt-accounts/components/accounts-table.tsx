import type {
  ColumnDef,
  OnChangeFn,
  PaginationState,
} from '@tanstack/react-table'
import { ExternalLink, LogIn, Pencil, RefreshCw, Trash2 } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { formatTimestamp } from '@/lib/format'

import { getAccountErrorMessage } from '../lib/account-error'
import type { GptAccount } from '../types'

export type AccountAction = 'edit' | 'login' | 'oauth' | 'refresh' | 'delete'
interface AccountsTableProps {
  items: GptAccount[]
  total: number
  pagination: PaginationState
  onPaginationChange: OnChangeFn<PaginationState>
  isLoading: boolean
  isFetching: boolean
  pending: boolean
  onAction: (account: GptAccount, action: AccountAction) => void
}

export function AccountsTable(props: AccountsTableProps) {
  const { t } = useTranslation()
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
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const status = row.original.status
          let label = t('Failed')
          let variant: 'info' | 'success' | 'warning' | 'danger' = 'danger'
          if (status === 'pending') {
            label = t('Awaiting authorization')
            variant = 'info'
          }
          if (status === 'logging_in') {
            label = t('Signing in')
            variant = 'info'
          }
          if (status === 'ready') {
            label = t('Ready')
            variant = 'success'
          }
          if (status === 'needs_attention') {
            label = t('Needs attention')
            variant = 'warning'
          }
          return (
            <div className='space-y-1' aria-live='polite'>
              <StatusBadge
                label={label}
                variant={variant}
                copyable={false}
                pulse={status === 'logging_in'}
              />
              {row.original.last_error_code && (
                <p className='text-muted-foreground max-w-64 text-xs'>
                  {getAccountErrorMessage(row.original.last_error_code, t)}
                </p>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'channel_id',
        header: t('Channel'),
        cell: ({ row }) =>
          row.original.channel_id ? (
            <span>
              {row.original.channel_name || `#${row.original.channel_id}`}
            </span>
          ) : (
            <span className='text-muted-foreground'>
              {t('Created after authorization')}
            </span>
          ),
      },
      {
        accessorKey: 'auto_relogin',
        header: t('Automatic sign-in'),
        cell: ({ row }) =>
          t(row.original.auto_relogin ? 'Enabled' : 'Disabled'),
      },
      {
        accessorKey: 'last_login_at',
        header: t('Last successful sign-in'),
        cell: ({ row }) => formatTimestamp(row.original.last_login_at),
      },
      {
        accessorKey: 'next_retry_at',
        header: t('Next retry'),
        cell: ({ row }) =>
          row.original.auto_relogin &&
          row.original.status === 'error' &&
          row.original.next_retry_at * 1000 > Date.now()
            ? formatTimestamp(row.original.next_retry_at)
            : '-',
      },
      {
        id: 'actions',
        header: t('Actions'),
        enableHiding: false,
        cell: ({ row }) => {
          const item = row.original
          const busy = props.pending || item.status === 'logging_in'
          return (
            <div className='flex items-center gap-1'>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Edit {{name}}', { name: item.name })}
                disabled={!item.can_manage || props.pending}
                onClick={() => props.onAction(item, 'edit')}
              >
                <Pencil />
              </Button>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Sign in {{name}}', { name: item.name })}
                disabled={!item.can_manage || busy}
                onClick={() => props.onAction(item, 'login')}
              >
                <LogIn />
              </Button>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Browser sign-in for {{name}}', {
                  name: item.name,
                })}
                disabled={!item.can_manage || busy}
                onClick={() => props.onAction(item, 'oauth')}
              >
                <ExternalLink />
              </Button>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Refresh credentials for {{name}}', {
                  name: item.name,
                })}
                disabled={!item.can_manage || busy || !item.channel_id}
                onClick={() => props.onAction(item, 'refresh')}
              >
                <RefreshCw />
              </Button>
              <Button
                variant='ghost'
                size='icon-sm'
                aria-label={t('Delete {{name}}', { name: item.name })}
                disabled={!item.can_manage || props.pending}
                onClick={() => props.onAction(item, 'delete')}
              >
                <Trash2 />
              </Button>
            </div>
          )
        },
      },
    ],
    [props, t]
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
