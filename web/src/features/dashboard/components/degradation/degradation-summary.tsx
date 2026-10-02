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
import {
  AlertTriangle,
  Coins,
  Percent,
  Radio,
  Users,
  type LucideIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { getMismatchRatePercent } from '@/features/dashboard/lib'
import type { ResponseModelMismatchSummary } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatPercent, formatQuota } from '@/lib/format'

interface DegradationSummaryProps {
  summary?: ResponseModelMismatchSummary
  loading: boolean
  error: boolean
}

interface SummaryItem {
  key: string
  title: string
  value: string
  description: string
  icon: LucideIcon
  iconTone: IconBadgeTone
}

export function DegradationSummary(props: DegradationSummaryProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const summary = props.summary
  const rate = summary
    ? getMismatchRatePercent(summary.mismatch_requests, summary.total_requests)
    : null

  const items: SummaryItem[] = [
    {
      key: 'requests',
      title: t('Degraded Requests'),
      value: formatNumber(summary?.mismatch_requests ?? 0, locale),
      description: t('Responses that declared a substituted model'),
      icon: AlertTriangle,
      iconTone: 'warning',
    },
    {
      key: 'rate',
      title: t('Degradation Rate'),
      value: formatPercent(rate),
      description: t('Of {{count}} consumed requests', {
        count: formatNumber(summary?.total_requests ?? 0, locale),
      }),
      icon: Percent,
      iconTone: 'chart-1',
    },
    {
      key: 'channels',
      title: t('Affected Channels'),
      value: formatNumber(summary?.affected_channels ?? 0, locale),
      description: t('Channels that returned a substituted model'),
      icon: Radio,
      iconTone: 'chart-2',
    },
    {
      key: 'users',
      title: t('Affected Users'),
      value: formatNumber(summary?.affected_users ?? 0, locale),
      description: t('Users who received a substituted model'),
      icon: Users,
      iconTone: 'chart-3',
    },
    {
      key: 'quota',
      title: t('Degraded Spend'),
      value: formatQuota(summary?.mismatch_quota ?? 0),
      description: t('Quota charged for degraded requests'),
      icon: Coins,
      iconTone: 'chart-4',
    },
  ]

  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='divide-border/60 grid min-w-0 grid-cols-2 divide-x sm:grid-cols-3 lg:grid-cols-5'>
        {items.map((item, idx) => {
          const Icon = item.icon
          let valueContent
          if (props.loading) {
            valueContent = (
              <Skeleton className='mt-1 h-5 w-16 sm:mt-2 sm:h-7 sm:w-20' />
            )
          } else {
            valueContent = (
              <div className='text-foreground mt-1 font-mono text-base leading-tight font-semibold tracking-tight tabular-nums sm:mt-2 sm:text-2xl sm:leading-normal'>
                {props.error ? '--' : item.value}
              </div>
            )
          }
          return (
            <div
              key={item.key}
              className={
                idx === items.length - 1
                  ? 'col-span-2 min-w-0 px-2.5 py-1.5 sm:col-span-1 sm:px-5 sm:py-4'
                  : 'min-w-0 px-2.5 py-1.5 sm:px-5 sm:py-4'
              }
            >
              <div className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium sm:gap-2'>
                <IconBadge tone={item.iconTone} size='stat'>
                  <Icon />
                </IconBadge>
                <span className='truncate'>{item.title}</span>
              </div>
              {valueContent}
              <div className='text-muted-foreground/60 mt-1 hidden truncate text-xs md:block'>
                {item.description}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
