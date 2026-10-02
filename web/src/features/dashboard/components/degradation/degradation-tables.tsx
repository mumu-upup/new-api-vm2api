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
import { ArrowRight, Radio, Shuffle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { TimestampCell } from '@/components/activity-time-cell'
import {
  BadgeCell,
  StaticDataTable,
  staticDataTableClassNames,
} from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { IconBadge } from '@/components/ui/icon-badge'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import { getMismatchRatePercent } from '@/features/dashboard/lib'
import type {
  ResponseModelMismatchChannel,
  ResponseModelMismatchPair,
} from '@/features/dashboard/types'
import { ModelBadge } from '@/features/usage-logs/components/model-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatPercent } from '@/lib/format'

interface DegradationChannelTableProps {
  channels: ResponseModelMismatchChannel[]
  loading: boolean
}

export function DegradationChannelTable(props: DegradationChannelTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='chart-2' size='sm'>
            <Radio />
          </IconBadge>
          {t('Degraded Channels')}
        </span>
      }
      loading={props.loading}
      contentClassName='p-0 sm:p-0'
    >
      <StaticDataTable
        className={staticDataTableClassNames.embeddedContainer}
        tableClassName='min-w-[560px]'
        data={props.channels}
        getRowKey={(channel) => channel.channel_id}
        emptyContent={t('No Data')}
        columns={[
          {
            id: 'channel',
            header: t('Channel'),
            cell: (channel) => (
              <div className='min-w-0'>
                <div className='truncate font-medium'>
                  {channel.channel_name || t('Unknown')}
                </div>
                <div className='text-muted-foreground font-mono text-xs'>
                  #{channel.channel_id}
                </div>
              </div>
            ),
          },
          {
            id: 'requests',
            header: t('Degraded / Total'),
            cellClassName: 'tabular-nums whitespace-nowrap',
            cell: (channel) =>
              `${formatNumber(channel.mismatch_requests, locale)} / ${formatNumber(channel.total_requests, locale)}`,
          },
          {
            id: 'rate',
            header: t('Degradation Rate'),
            cellClassName: 'tabular-nums font-medium',
            cell: (channel) =>
              formatPercent(
                getMismatchRatePercent(
                  channel.mismatch_requests,
                  channel.total_requests
                )
              ),
          },
          {
            id: 'returned',
            header: t('Returned Models'),
            cell: (channel) => (
              <BadgeCell>
                {channel.returned_models.map((name) => (
                  <StatusBadge
                    key={name}
                    label={name}
                    variant='neutral'
                    copyable={false}
                  />
                ))}
              </BadgeCell>
            ),
          },
          {
            id: 'last-seen',
            header: t('Last Occurred'),
            cell: (channel) => (
              <TimestampCell
                timestamp={channel.last_seen_at}
                locale={locale}
                justNowLabel={t('Just now')}
                className='text-muted-foreground'
              />
            ),
          },
        ]}
      />
    </PanelWrapper>
  )
}

interface DegradationPairTableProps {
  pairs: ResponseModelMismatchPair[]
  loading: boolean
}

export function DegradationPairTable(props: DegradationPairTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='chart-1' size='sm'>
            <Shuffle />
          </IconBadge>
          {t('Model Substitutions')}
        </span>
      }
      loading={props.loading}
      contentClassName='p-0 sm:p-0'
    >
      <StaticDataTable
        className={staticDataTableClassNames.embeddedContainer}
        tableClassName='min-w-[520px]'
        data={props.pairs}
        getRowKey={(pair) => `${pair.requested_model}→${pair.returned_model}`}
        emptyContent={t('No Data')}
        columns={[
          {
            id: 'models',
            header: `${t('Request Model')} → ${t('Response Model')}`,
            cell: (pair) => (
              <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
                <ModelBadge modelName={pair.requested_model} />
                <ArrowRight
                  className='text-muted-foreground size-3.5 shrink-0'
                  aria-hidden='true'
                />
                <ModelBadge modelName={pair.returned_model} />
              </div>
            ),
          },
          {
            id: 'requests',
            header: t('Requests'),
            cellClassName: 'tabular-nums',
            cell: (pair) => formatNumber(pair.requests, locale),
          },
          {
            id: 'channels',
            header: t('Channels'),
            cellClassName: 'tabular-nums',
            cell: (pair) => formatNumber(pair.channels, locale),
          },
          {
            id: 'last-seen',
            header: t('Last Occurred'),
            cell: (pair) => (
              <TimestampCell
                timestamp={pair.last_seen_at}
                locale={locale}
                justNowLabel={t('Just now')}
                className='text-muted-foreground'
              />
            ),
          },
        ]}
      />
    </PanelWrapper>
  )
}
