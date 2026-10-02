/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Link } from '@tanstack/react-router'
import { History } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { TimestampCell } from '@/components/activity-time-cell'
import {
  StaticDataTable,
  staticDataTableClassNames,
} from '@/components/data-table'
import { IconBadge } from '@/components/ui/icon-badge'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import type { ResponseModelMismatchItem } from '@/features/dashboard/types'
import { ModelBadge } from '@/features/usage-logs/components/model-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'

// Usage-log search window around a record, in milliseconds.
const USAGE_LOG_WINDOW_MS = 60_000

interface DegradationRecentTableProps {
  items: ResponseModelMismatchItem[]
  itemLimit: number
  loading: boolean
}

export function DegradationRecentTable(props: DegradationRecentTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='warning' size='sm'>
            <History />
          </IconBadge>
          {t('Recent Degraded Requests')}
        </span>
      }
      description={t('Showing up to the latest {{count}} records', {
        count: props.itemLimit,
      })}
      loading={props.loading}
      contentClassName='p-0 sm:p-0'
    >
      <StaticDataTable
        className={staticDataTableClassNames.embeddedContainer}
        tableClassName='min-w-[900px]'
        data={props.items}
        getRowKey={(item) => item.id}
        emptyContent={t('No Data')}
        columns={[
          {
            id: 'time',
            header: t('Time'),
            cell: (item) => (
              <TimestampCell
                timestamp={item.created_at}
                locale={locale}
                justNowLabel={t('Just now')}
              />
            ),
          },
          {
            id: 'user',
            header: t('User'),
            cell: (item) => (
              <div className='min-w-0'>
                <div className='truncate'>{item.username || '-'}</div>
                <div className='text-muted-foreground truncate text-xs'>
                  {item.token_name || '-'}
                </div>
              </div>
            ),
          },
          {
            id: 'channel',
            header: t('Channel'),
            cell: (item) => (
              <div className='min-w-0'>
                <div className='truncate'>
                  {item.channel_name || t('Unknown')}
                </div>
                <div className='text-muted-foreground font-mono text-xs'>
                  #{item.channel_id}
                </div>
              </div>
            ),
          },
          {
            id: 'model',
            header: t('Model'),
            cell: (item) => (
              <ModelBadge
                modelName={item.requested_model}
                responseModel={{
                  requested_model: item.requested_model,
                  upstream_model: item.upstream_model,
                  returned_model: item.returned_model,
                }}
              />
            ),
          },
          {
            id: 'tokens',
            header: t('Tokens'),
            cellClassName: 'tabular-nums whitespace-nowrap',
            cell: (item) =>
              `${formatNumber(item.prompt_tokens, locale)} / ${formatNumber(item.completion_tokens, locale)}`,
          },
          {
            id: 'quota',
            header: t('Quota'),
            cellClassName: 'tabular-nums whitespace-nowrap',
            cell: (item) => formatQuota(item.quota),
          },
          {
            id: 'request',
            header: t('Request ID'),
            cell: (item) =>
              item.request_id ? (
                <Link
                  to='/usage-logs/$section'
                  params={{ section: 'common' }}
                  search={{
                    requestId: item.request_id,
                    startTime: item.created_at * 1000 - USAGE_LOG_WINDOW_MS,
                    endTime: item.created_at * 1000 + USAGE_LOG_WINDOW_MS,
                  }}
                  title={t('View in usage logs')}
                  className='text-primary block max-w-48 truncate font-mono text-xs hover:underline'
                >
                  {item.request_id}
                </Link>
              ) : (
                <span className='text-muted-foreground'>-</span>
              ),
          },
        ]}
      />
    </PanelWrapper>
  )
}
