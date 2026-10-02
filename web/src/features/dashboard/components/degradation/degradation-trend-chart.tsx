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
import { VChart } from '@visactor/react-vchart'
import { TrendingUp } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import { getDashboardChartColors } from '@/features/dashboard/lib/charts'
import type { DegradationTrendPoint } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

interface DegradationTrendChartProps {
  points: DegradationTrendPoint[]
  loading: boolean
}

export function DegradationTrendChart(props: DegradationTrendChartProps) {
  const { t, i18n } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const seriesName = t('Degraded Requests')

  const spec = useMemo(() => {
    const textColor =
      resolvedTheme === 'dark'
        ? 'rgba(255, 255, 255, 0.65)'
        : 'rgba(15, 23, 42, 0.65)'
    const gridColor =
      resolvedTheme === 'dark'
        ? 'rgba(255, 255, 255, 0.12)'
        : 'rgba(15, 23, 42, 0.12)'
    // One row shared by mark and column hover, so hovering anywhere in a
    // time bucket shows its count.
    const tooltipRow = {
      key: seriesName,
      value: (datum: Record<string, unknown>) =>
        formatNumber(Number(datum?.requests) || 0, locale),
    }
    return {
      type: 'bar' as const,
      data: [{ id: 'degradation-trend', values: props.points }],
      xField: 'label',
      yField: 'requests',
      color: getDashboardChartColors(1).slice(0, 1),
      legends: { visible: false },
      bar: { style: { cornerRadius: [4, 4, 0, 0] } },
      barMaxWidth: 28,
      axes: [
        {
          orient: 'bottom',
          label: {
            style: { fill: textColor, fontSize: 10 },
            autoHide: true,
            autoLimit: true,
          },
          tick: { visible: false },
        },
        {
          orient: 'left',
          label: {
            formatMethod: (value: number | string) =>
              formatNumber(Number(value), locale),
            style: { fill: textColor, fontSize: 10 },
          },
          grid: {
            visible: true,
            style: { lineDash: [3, 3], stroke: gridColor },
          },
        },
      ],
      tooltip: {
        mark: { content: [tooltipRow] },
        dimension: {
          title: {
            value: (datum: Record<string, unknown>) =>
              String(datum?.label ?? ''),
          },
          content: [tooltipRow],
        },
      },
      animation: false,
    }
  }, [props.points, resolvedTheme, locale, seriesName])

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='warning' size='sm'>
            <TrendingUp />
          </IconBadge>
          {t('Degraded Requests Trend')}
        </span>
      }
      loading={props.loading}
      height='h-72'
      contentClassName='h-72 p-1.5 sm:p-2'
    >
      {themeReady && (
        <VChart
          key={`degradation-trend-${resolvedTheme}`}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </PanelWrapper>
  )
}
