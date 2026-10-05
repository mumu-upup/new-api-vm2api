import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'

import type { GptAccountGateway } from '../types'

export function GatewaysPanel(props: {
  items: GptAccountGateway[]
  isLoading: boolean
  isError: boolean
  encryptionAvailable: boolean
  onRetry: () => void
  onEdit: (gateway: GptAccountGateway | 'create') => void
}) {
  const { t } = useTranslation()
  let content: ReactNode
  if (props.isLoading) content = <LoadingState />
  else if (props.isError) content = <ErrorState onRetry={props.onRetry} />
  else {
    content = (
      <StaticDataTable
        data={props.items}
        getRowKey={(gateway) => gateway.id}
        emptyContent={t('No gateways configured')}
        columns={[
          { id: 'name', header: t('Name'), cell: (gateway) => gateway.name },
          {
            id: 'provider',
            header: t('Provider'),
            cell: (gateway) => (gateway.provider === 'cpa' ? 'CPA' : 'Sub2API'),
          },
          {
            id: 'channel',
            header: t('Channel'),
            cell: (gateway) =>
              gateway.channel_name ||
              (gateway.channel_id ? `#${gateway.channel_id}` : '-'),
          },
          {
            id: 'management',
            header: t('Management key'),
            cell: (gateway) =>
              gateway.has_management_key
                ? t('Configured')
                : t('Not configured'),
          },
          {
            id: 'relay',
            header: t('Relay key'),
            cell: (gateway) =>
              gateway.has_relay_key ? t('Configured') : t('Not configured'),
          },
          {
            id: 'actions',
            header: t('Actions'),
            cell: (gateway) => (
              <Button
                variant='outline'
                size='sm'
                aria-label={t('Edit gateway {{name}}', { name: gateway.name })}
                disabled={!props.encryptionAvailable}
                onClick={() => props.onEdit(gateway)}
              >
                {t('Edit')}
              </Button>
            ),
          },
        ]}
      />
    )
  }
  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Accounts added to this gateway share its channel. Requests may use any available account in the gateway.'
          )}
        </p>
        <Button
          onClick={() => props.onEdit('create')}
          disabled={!props.encryptionAvailable}
        >
          {t('Add gateway')}
        </Button>
      </div>
      {content}
    </div>
  )
}
