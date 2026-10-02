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
import { describe, expect, it } from 'vitest'

import {
  buildDegradationTrend,
  getDegradationTimeRange,
  getMismatchRatePercent,
} from '../degradation'

const unix = (date: Date) => Math.floor(date.getTime() / 1000)

describe('getDegradationTimeRange', () => {
  it('returns the trailing 24 hours for the day range', () => {
    const now = new Date(2026, 9, 2, 16, 30)

    expect(getDegradationTimeRange('day', now)).toEqual({
      start_timestamp: unix(now) - 24 * 3600,
      end_timestamp: unix(now),
    })
  })

  it.each([
    ['Monday morning', new Date(2026, 8, 28, 0, 0, 1)],
    ['Wednesday afternoon', new Date(2026, 8, 30, 15, 4)],
    ['Sunday night', new Date(2026, 9, 4, 23, 59)],
  ])('starts the week range at local Monday 00:00 on %s', (_, now) => {
    expect(getDegradationTimeRange('week', now)).toEqual({
      start_timestamp: unix(new Date(2026, 8, 28)),
      end_timestamp: unix(now),
    })
  })
})

describe('buildDegradationTrend', () => {
  it('fills every hour of the day range and keeps bucket counts', () => {
    const start = 1_790_900_000
    const firstHour = start - (start % 3600)
    const points = buildDegradationTrend(
      [{ timestamp: firstHour + 3600, requests: 4 }],
      'day',
      { start_timestamp: start, end_timestamp: start + 24 * 3600 }
    )

    expect(points).toHaveLength(25)
    expect(points[0]).toMatchObject({ timestamp: firstHour, requests: 0 })
    expect(points[1]).toMatchObject({
      timestamp: firstHour + 3600,
      requests: 4,
    })
    expect(points.reduce((sum, p) => sum + p.requests, 0)).toBe(4)
  })

  it('sums hourly buckets into local days from Monday to today for the week range', () => {
    const monday = new Date(2026, 8, 28)
    const points = buildDegradationTrend(
      [
        { timestamp: unix(new Date(2026, 8, 28, 1)), requests: 2 },
        { timestamp: unix(new Date(2026, 8, 28, 5)), requests: 1 },
        { timestamp: unix(new Date(2026, 8, 30, 10)), requests: 5 },
      ],
      'week',
      {
        start_timestamp: unix(monday),
        end_timestamp: unix(new Date(2026, 8, 30, 12)),
      }
    )

    expect(points.map((p) => [p.timestamp, p.requests])).toEqual([
      [unix(monday), 3],
      [unix(new Date(2026, 8, 29)), 0],
      [unix(new Date(2026, 8, 30)), 5],
    ])
  })

  it('returns zero-filled days when nothing was degraded this week', () => {
    const points = buildDegradationTrend([], 'week', {
      start_timestamp: unix(new Date(2026, 8, 28)),
      end_timestamp: unix(new Date(2026, 8, 28, 9)),
    })

    expect(points).toHaveLength(1)
    expect(points[0].requests).toBe(0)
  })
})

describe('getMismatchRatePercent', () => {
  it('returns the share of degraded requests as a percentage', () => {
    expect(getMismatchRatePercent(3, 4)).toBe(75)
  })

  it.each([
    ['no consume logs', 2, 0],
    ['more mismatches than logged requests', 5, 3],
  ])('returns null with %s', (_, mismatch, total) => {
    expect(getMismatchRatePercent(mismatch, total)).toBeNull()
  })
})
