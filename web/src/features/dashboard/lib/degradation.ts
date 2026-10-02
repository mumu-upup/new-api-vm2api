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
import dayjs from '@/lib/dayjs'
import { formatChartTime } from '@/lib/time'

import type {
  DegradationRange,
  DegradationTimeRange,
  DegradationTrendPoint,
  ResponseModelMismatchBucket,
} from '../types'

const HOUR_SECONDS = 3600
const DAY_SECONDS = 24 * HOUR_SECONDS

/**
 * Time window for the degradation dashboard. "week" starts at local Monday
 * 00:00, matching the backend's weekly cleanup that keeps only this week.
 */
export function getDegradationTimeRange(
  range: DegradationRange,
  now: Date = new Date()
): DegradationTimeRange {
  const end = Math.floor(now.getTime() / 1000)
  if (range === 'day') {
    return { start_timestamp: end - DAY_SECONDS, end_timestamp: end }
  }
  const daysSinceMonday = (now.getDay() + 6) % 7
  const monday = new Date(
    now.getFullYear(),
    now.getMonth(),
    now.getDate() - daysSinceMonday
  )
  return {
    start_timestamp: Math.floor(monday.getTime() / 1000),
    end_timestamp: end,
  }
}

/**
 * Expand the backend's sparse hourly buckets into a gap-free series: one point
 * per hour for the 24-hour view, one point per local day for the week view.
 */
export function buildDegradationTrend(
  buckets: ResponseModelMismatchBucket[],
  range: DegradationRange,
  timeRange: DegradationTimeRange
): DegradationTrendPoint[] {
  const points: DegradationTrendPoint[] = []
  if (range === 'day') {
    const counts = new Map(buckets.map((b) => [b.timestamp, b.requests]))
    const first =
      Math.floor(timeRange.start_timestamp / HOUR_SECONDS) * HOUR_SECONDS
    for (let ts = first; ts <= timeRange.end_timestamp; ts += HOUR_SECONDS) {
      points.push({
        timestamp: ts,
        label: formatChartTime(ts, 'hour'),
        requests: counts.get(ts) ?? 0,
      })
    }
    return points
  }

  const counts = new Map<number, number>()
  for (const bucket of buckets) {
    const day = dayjs(bucket.timestamp * 1000)
      .startOf('day')
      .unix()
    counts.set(day, (counts.get(day) ?? 0) + bucket.requests)
  }
  const lastDay = dayjs(timeRange.end_timestamp * 1000).startOf('day')
  for (
    let day = dayjs(timeRange.start_timestamp * 1000).startOf('day');
    !day.isAfter(lastDay);
    day = day.add(1, 'day')
  ) {
    const ts = day.unix()
    points.push({
      timestamp: ts,
      label: formatChartTime(ts, 'day'),
      requests: counts.get(ts) ?? 0,
    })
  }
  return points
}

/**
 * Share of a channel's requests that came back with a substituted model, as a
 * percentage. Returns null when the consume-log total cannot back the rate
 * (no logged requests, or fewer than the recorded mismatches, e.g. when
 * consume logging is off).
 */
export function getMismatchRatePercent(
  mismatchRequests: number,
  totalRequests: number
): number | null {
  if (totalRequests <= 0 || mismatchRequests > totalRequests) return null
  return (mismatchRequests / totalRequests) * 100
}
