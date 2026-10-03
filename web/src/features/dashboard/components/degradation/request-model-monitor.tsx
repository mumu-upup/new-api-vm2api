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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { TimestampCell } from '@/components/activity-time-cell'
import {
  StaticDataTable,
  staticDataTableClassNames,
} from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import { getDegradationTimeRange } from '@/features/dashboard/lib'
import type { DegradationRange } from '@/features/dashboard/types'
import { getAllLogs } from '@/features/usage-logs/api'
import {
  ModelBadge,
  ResponseModelStatus,
} from '@/features/usage-logs/components/model-badge'
import type { UsageLog } from '@/features/usage-logs/data/schema'
import { parseLogOther } from '@/features/usage-logs/lib/format'
import { getResponseModelStatus } from '@/features/usage-logs/lib/response-model'
import { toIntlLocale } from '@/i18n/languages'
import { requireServerSuccess } from '@/lib/server-error-message'

export function RequestModelMonitor(props: {
  range: DegradationRange
  requestedAt: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = useQuery({
    queryKey: [
      'dashboard',
      'request-model-monitor',
      props.range,
      props.requestedAt,
    ],
    queryFn: async () => {
      const response = requireServerSuccess(
        await getAllLogs({
          ...getDegradationTimeRange(props.range),
          type: 2,
          p: 1,
          page_size: 100,
        })
      )
      return ((response.data?.items as UsageLog[] | undefined) ?? [])
        .map((log) => ({
          log,
          observation: parseLogOther(log.other)?.response_model,
        }))
        .filter((item) => {
          const status = getResponseModelStatus(item.observation)
          return status === 'reported' || status === 'mismatch'
        })
    },
    staleTime: 5_000,
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
  })

  return (
    <PanelWrapper
      title={t('Recent anomalous requests')}
      description={t(
        'Only model mismatches or upstream-reported degradation among the latest {{count}} completed requests in this range. Refreshes every 10 seconds; requires consumption logging.',
        { count: 100 }
      )}
      loading={query.isLoading}
      contentClassName='p-0 sm:p-0'
    >
      {query.isError ? (
        <ErrorState onRetry={() => void query.refetch()} />
      ) : (
        <StaticDataTable
          className={staticDataTableClassNames.embeddedContainer}
          tableClassName='min-w-[850px]'
          data={query.data ?? []}
          getRowKey={(item) => item.log.id}
          emptyContent={t(
            'No model mismatch or upstream-reported degradation in the latest completed request logs.'
          )}
          columns={[
            {
              id: 'time',
              header: t('Time'),
              cell: (item) => (
                <TimestampCell
                  timestamp={item.log.created_at}
                  locale={locale}
                  justNowLabel={t('Just now')}
                />
              ),
            },
            {
              id: 'channel',
              header: t('Channel'),
              cell: (item) => (
                <span>
                  {item.log.channel_name || t('Unknown')} #{item.log.channel}
                </span>
              ),
            },
            {
              id: 'model',
              header: t('Model'),
              cell: (item) => (
                <ModelBadge
                  modelName={item.log.model_name}
                  responseModel={item.observation}
                />
              ),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (item) => (
                <ResponseModelStatus observation={item.observation} />
              ),
            },
            {
              id: 'actual',
              header: t('Upstream declared actual model'),
              cell: (item) =>
                item.observation?.actual_model ||
                item.observation?.returned_model ||
                t('Not reported'),
            },
            {
              id: 'reason',
              header: t('Degradation reason'),
              cellClassName: 'max-w-64 whitespace-normal break-words',
              cell: (item) => item.observation?.degrade_reason || '-',
            },
            {
              id: 'request',
              header: t('Request ID'),
              cell: (item) =>
                item.log.request_id ? (
                  <Link
                    to='/usage-logs/$section'
                    params={{ section: 'common' }}
                    search={{
                      requestId: item.log.request_id,
                      startTime: item.log.created_at * 1000 - 60_000,
                      endTime: item.log.created_at * 1000 + 60_000,
                    }}
                    title={t('View in usage logs')}
                    className='text-primary block max-w-48 truncate font-mono text-xs hover:underline'
                  >
                    {item.log.request_id}
                  </Link>
                ) : (
                  '-'
                ),
            },
          ]}
        />
      )}
    </PanelWrapper>
  )
}
