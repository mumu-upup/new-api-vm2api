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
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Route as MonitorRoute } from '@/routes/_authenticated/channel-monitor'
import { Route as DashboardRoute } from '@/routes/_authenticated/dashboard/$section'
import { useAuthStore } from '@/stores/auth-store'

import { ChannelMonitorPage } from '../index'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))
vi.mock('@/features/dashboard', () => ({ Dashboard: () => null }))
vi.mock('@/features/dashboard/components/channel-monitor/channel-monitor-dashboard', () => ({
  ChannelMonitorDashboard: () => <button type='button'>Run monitor</button>,
}))

afterEach(() => useAuthStore.getState().auth.reset())

describe('standalone channel monitor navigation', () => {
  it('renders monitoring without dashboard navigation tabs', () => {
    render(<ChannelMonitorPage />)
    expect(screen.getByRole('heading', { name: 'Channel monitor' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Run monitor' })).toBeVisible()
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
  })

  it('redirects old dashboard bookmarks to the standalone page', () => {
    expect(() => DashboardRoute.options.beforeLoad?.({
      params: { section: 'channel-monitor' },
    } as never)).toThrow(expect.objectContaining({
      options: expect.objectContaining({ to: '/channel-monitor', replace: true }),
    }))
  })

  it.each([0, 1, 10, 100])('preserves admin-only access for role %s', (role) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'test', role })
    const enter = () => MonitorRoute.options.beforeLoad?.({} as never)
    if (role < 10) {
      expect(enter).toThrow(expect.objectContaining({
        options: expect.objectContaining({ to: '/403' }),
      }))
    } else {
      expect(enter).not.toThrow()
    }
  })
})
