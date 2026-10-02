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
import { Info, Loader2, RefreshCw, ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getResponseModelMismatchStats } from '@/features/dashboard/api'
import {
  buildDegradationTrend,
  getDegradationTimeRange,
} from '@/features/dashboard/lib'
import type { DegradationRange } from '@/features/dashboard/types'
import { formatTimestampToDate } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { DegradationRecentTable } from './degradation-recent-table'
import { DegradationSummary } from './degradation-summary'
import {
  DegradationChannelTable,
  DegradationPairTable,
} from './degradation-tables'
import { DegradationTrendChart } from './degradation-trend-chart'

export function DegradationDashboard() {
  const { t } = useTranslation()
  const [range, setRange] = useState<DegradationRange>('day')
  const [requestedAt, setRequestedAt] = useState(() => Date.now())

  const timeRange = useMemo(
    () => getDegradationTimeRange(range, new Date(requestedAt)),
    [range, requestedAt]
  )

  const statsQuery = useQuery({
    queryKey: ['dashboard', 'response-model-mismatch', timeRange],
    queryFn: async () =>
      requireServerSuccess(await getResponseModelMismatchStats(timeRange)),
    select: (res) => res.data,
    staleTime: 60_000,
  })

  const stats = statsQuery.data
  const loading = statsQuery.isLoading
  const trend = useMemo(
    () => buildDegradationTrend(stats?.trend ?? [], range, timeRange),
    [stats, range, timeRange]
  )
  const hasMismatches = (stats?.summary.mismatch_requests ?? 0) > 0

  const handleRangeChange = (value: string) => {
    setRange(value as DegradationRange)
    setRequestedAt(Date.now())
  }

  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex flex-wrap items-center gap-1.5 sm:gap-2'>
        <Tabs value={range} onValueChange={handleRangeChange}>
          <TabsList>
            <TabsTrigger value='day' className='px-2.5 text-xs'>
              {t('24 Hours')}
            </TabsTrigger>
            <TabsTrigger value='week' className='px-2.5 text-xs'>
              {t('This week')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <Button
          variant='ghost'
          size='icon'
          className='text-muted-foreground hover:text-foreground size-8'
          aria-label={t('Refresh')}
          title={t('Refresh')}
          disabled={statsQuery.isFetching}
          onClick={() => setRequestedAt(Date.now())}
        >
          {statsQuery.isFetching ? (
            <Loader2 className='animate-spin' />
          ) : (
            <RefreshCw />
          )}
        </Button>
      </div>

      <p className='text-muted-foreground flex items-start gap-1.5 text-xs leading-relaxed'>
        <Info className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
        <span>
          {t(
            'A request counts as degraded when the upstream response declares a model that matches neither the requested model nor the mapped upstream model.'
          )}{' '}
          {stats
            ? t(
                'Only the current week (since {{date}}) is kept; the previous week is cleared every Monday at 00:00 (server time).',
                { date: formatTimestampToDate(stats.summary.retained_since) }
              )
            : null}
        </span>
      </p>

      <DegradationSummary
        summary={stats?.summary}
        loading={loading}
        error={statsQuery.isError}
      />

      {statsQuery.isError && (
        <ErrorState
          className='border'
          onRetry={() => void statsQuery.refetch()}
        />
      )}
      {!loading && !statsQuery.isError && !hasMismatches && (
        <EmptyState
          bordered
          icon={ShieldCheck}
          title={t('No degraded requests')}
          description={t(
            'No upstream returned a substituted model in this period.'
          )}
        />
      )}
      {(loading || hasMismatches) && (
        <>
          <DegradationTrendChart points={trend} loading={loading} />
          <div className='grid gap-3 sm:gap-4 xl:grid-cols-2'>
            <DegradationChannelTable
              channels={stats?.channels ?? []}
              loading={loading}
            />
            <DegradationPairTable
              pairs={stats?.pairs ?? []}
              loading={loading}
            />
          </div>
          <DegradationRecentTable
            items={stats?.items ?? []}
            itemLimit={stats?.summary.item_limit ?? 0}
            loading={loading}
          />
        </>
      )}
    </div>
  )
}
