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
import { act, render, renderHook, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { PricingSidebar } from '../components/pricing-sidebar'
import {
  PricingToolbar,
  type PricingToolbarProps,
} from '../components/pricing-toolbar'
import { useFilters } from '../hooks/use-filters'
import type { PricingCatalogVendor, PricingModel } from '../types'

vi.mock('@tanstack/react-router', () => ({ useSearch: () => ({}) }))

function toolbarProps(): PricingToolbarProps {
  return {
    filteredCount: 2,
    totalCount: 2,
    sortBy: 'name',
    tokenUnit: 'M',
    showRechargePrice: false,
    viewMode: 'card',
    quotaTypeFilter: 'all',
    endpointTypeFilter: 'all',
    vendorFilter: 'all',
    groupFilter: 'all',
    tagFilter: 'all',
    onSortChange: vi.fn(),
    onTokenUnitChange: vi.fn(),
    onRechargePriceChange: vi.fn(),
    onViewModeChange: vi.fn(),
    onQuotaTypeChange: vi.fn(),
    onEndpointTypeChange: vi.fn(),
    onVendorChange: vi.fn(),
    onGroupChange: vi.fn(),
    onTagChange: vi.fn(),
    vendors: [],
    groups: ['default', 'premium'],
    groupRatios: { default: 1, premium: 3 },
    tags: [],
    models: [],
    hasActiveFilters: false,
    activeFilterCount: 0,
    onClearFilters: vi.fn(),
  }
}

describe('pricing controls', () => {
  it('selects group parents atomically and resets incompatible descendants on region and vendor changes', () => {
    const catalog: PricingCatalogVendor[] = [
      {
        vendor_id: 1,
        name: 'Domestic vendor',
        region: 'domestic',
        groups: [
          { name: 'pending', description: '', ratio: 1, model_count: 0 },
        ],
      },
      {
        vendor_id: 2,
        name: 'International vendor',
        region: 'international',
        groups: [
          { name: 'other-group', description: '', ratio: 2, model_count: 0 },
        ],
      },
    ]
    const { result } = renderHook(() => useFilters([], catalog))
    act(() => result.current.setGroupFilter('pending', 'Domestic vendor'))
    expect(result.current.vendorFilter).toBe('Domestic vendor')
    expect(result.current.regionFilter).toBe('domestic')
    expect(result.current.groupFilter).toBe('pending')
    act(() => result.current.setVendorFilter('International vendor'))
    expect(result.current.regionFilter).toBe('international')
    expect(result.current.groupFilter).toBe('all')
    act(() =>
      result.current.setGroupFilter('other-group', 'International vendor')
    )
    act(() => result.current.setRegionFilter('domestic'))
    expect(result.current.vendorFilter).toBe('all')
    expect(result.current.groupFilter).toBe('all')
    expect(result.current.hasActiveFilters).toBe(true)
    act(() => result.current.clearFilters())
    expect(result.current.regionFilter).toBe('all')
  })

  it('keeps legacy vendor filtering region-neutral when no catalog is configured', () => {
    const { result } = renderHook(() => useFilters([]))
    act(() => result.current.setVendorFilter('Legacy vendor'))
    expect(result.current.regionFilter).toBe('all')
    expect(result.current.activeFilterCount).toBe(1)
  })

  it('filters real models by region while keeping uncataloged models in Other', () => {
    const base: PricingModel = {
      id: 1,
      model_name: 'domestic',
      vendor_name: 'Domestic vendor',
      vendor_id: 1,
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['default'],
    }
    const models = [
      base,
      {
        ...base,
        id: 2,
        model_name: 'legacy',
        vendor_id: 2,
        vendor_name: 'Legacy vendor',
      },
    ]
    const catalog: PricingCatalogVendor[] = [
      { vendor_id: 1, name: 'Domestic vendor', region: 'domestic', groups: [] },
    ]
    const { result } = renderHook(() => useFilters(models, catalog))
    act(() => result.current.setRegionFilter('domestic'))
    expect(
      result.current.filteredModels.map((model) => model.model_name)
    ).toEqual(['domestic'])
    act(() => result.current.setRegionFilter('other'))
    expect(
      result.current.filteredModels.map((model) => model.model_name)
    ).toEqual(['legacy'])
    act(() => result.current.setRegionFilter('international'))
    expect(result.current.filteredModels).toEqual([])
  })

  it('exposes pending catalog groups in the shared mobile filter drawer with keyboard selection', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    render(
      <PricingToolbar
        {...props}
        regionFilter='domestic'
        onRegionChange={vi.fn()}
        catalog={[
          {
            vendor_id: 1,
            name: 'Pending vendor',
            region: 'domestic',
            groups: [
              { name: 'pending', description: '', ratio: 1, model_count: 0 },
            ],
          },
        ]}
      />
    )
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    const dialog = await screen.findByRole('dialog', { name: 'Filter' })
    const group = within(dialog).getByRole('button', {
      name: /pending.*1x.*Pending connection/i,
    })
    expect(
      within(dialog).getByRole('button', { name: 'Domestic' })
    ).toHaveAttribute('aria-pressed', 'true')
    group.focus()
    await user.keyboard('{Enter}')
    expect(props.onGroupChange).toHaveBeenCalledWith(
      'pending',
      'Pending vendor'
    )
  })

  it('shows pending catalog vendors and groups and selects the group with its parent', async () => {
    const props = toolbarProps()
    const onRegionChange = vi.fn()
    const user = userEvent.setup()
    render(
      <PricingSidebar
        {...props}
        regionFilter='all'
        onRegionChange={onRegionChange}
        catalog={[
          {
            vendor_id: 1,
            name: 'Pending vendor',
            region: 'domestic',
            groups: [
              { name: 'pending', description: '', ratio: 1.5, model_count: 0 },
            ],
          },
        ]}
      />
    )
    expect(
      screen.getByRole('button', { name: /^Pending vendor\s*0$/ })
    ).toBeVisible()
    const group = screen.getByRole('button', {
      name: /pending.*1.5x.*Pending connection/i,
    })
    await user.click(group)
    expect(props.onGroupChange).toHaveBeenCalledWith(
      'pending',
      'Pending vendor'
    )
    await user.click(screen.getByRole('button', { name: 'Domestic' }))
    expect(onRegionChange).toHaveBeenCalledWith('domestic')
  })

  it('retains uncataloged active vendors under Other when filtering regions', async () => {
    const props = toolbarProps()
    const model: PricingModel = {
      id: 1,
      model_name: 'legacy',
      vendor_name: 'Legacy vendor',
      vendor_id: 2,
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['default'],
    }
    const { rerender } = render(
      <PricingSidebar
        {...props}
        regionFilter='other'
        onRegionChange={vi.fn()}
        vendors={[{ id: 2, name: 'Legacy vendor' }]}
        models={[model]}
        catalog={[
          {
            vendor_id: 1,
            name: 'Pending vendor',
            region: 'domestic',
            groups: [],
          },
        ]}
      />
    )
    expect(
      screen.getByRole('button', { name: /^Legacy vendor\s*1$/ })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /^Pending vendor\s*0$/ })
    ).toBeNull()
    rerender(
      <PricingSidebar
        {...props}
        regionFilter='domestic'
        onRegionChange={vi.fn()}
        vendors={[{ id: 2, name: 'Legacy vendor' }]}
        models={[model]}
        catalog={[
          {
            vendor_id: 1,
            name: 'Pending vendor',
            region: 'domestic',
            groups: [],
          },
        ]}
      />
    )
    expect(
      screen.queryByRole('button', { name: /^Legacy vendor\s*1$/ })
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: /^Pending vendor\s*0$/ })
    ).toBeVisible()
  })

  it('counts each model once per filter and updates counts when the catalog changes', () => {
    const props = toolbarProps()
    const base: PricingModel = {
      id: 1,
      model_name: 'text-model',
      vendor_name: 'Vendor A',
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['default'],
      tags: 'Chat,chat',
      supported_endpoint_types: ['openai', 'openai'],
    }
    const models: PricingModel[] = [
      base,
      {
        ...base,
        id: 2,
        model_name: 'image-model',
        quota_type: 1,
        tags: 'chat,Image',
        supported_endpoint_types: ['image-generation'],
      },
      {
        ...base,
        id: 3,
        model_name: 'task-model',
        vendor_name: 'Vendor B',
        tags: 'Video',
        supported_endpoint_types: ['openai-video'],
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      },
    ]
    const sidebarProps = {
      ...props,
      vendors: [
        { id: 1, name: 'Vendor A' },
        { id: 2, name: 'Vendor B' },
      ],
      tags: ['Chat', 'Image', 'Video'],
    }
    const { rerender } = render(
      <PricingSidebar {...sidebarProps} models={models} />
    )

    expect(
      screen.getByRole('button', { name: /^All Vendors\s*3$/ })
    ).toBeVisible()
    expect(screen.getByRole('button', { name: /^Vendor A\s*2$/ })).toBeVisible()
    expect(screen.getByRole('button', { name: /^Vendor B\s*1$/ })).toBeVisible()
    expect(screen.getByRole('button', { name: /^Chat\s*2$/ })).toBeVisible()
    expect(screen.getByRole('button', { name: /^Chat\s*1$/ })).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^Token-based\s*1$/ })
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^Per Request\s*1$/ })
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^Task billing\s*1$/ })
    ).toBeVisible()

    rerender(<PricingSidebar {...sidebarProps} models={[models[1]]} />)
    expect(
      screen.getByRole('button', { name: /^All Vendors\s*1$/ })
    ).toBeVisible()
    expect(screen.getByRole('button', { name: /^Vendor A\s*1$/ })).toBeVisible()
    expect(screen.queryByRole('button', { name: /^Vendor B\s*1$/ })).toBeNull()
    expect(screen.getByRole('button', { name: /^Chat\s*1$/ })).toBeVisible()
    expect(screen.getByRole('button', { name: /^Chat\s*0$/ })).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^Token-based\s*0$/ })
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^Task billing\s*0$/ })
    ).toBeVisible()
  })

  it('changes the token unit and keeps the selected unit pressed when clicked again', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    const { rerender } = render(<PricingToolbar {...props} />)
    await user.click(screen.getByRole('button', { name: '/1K' }))
    expect(props.onTokenUnitChange).toHaveBeenCalledWith('K')
    rerender(<PricingToolbar {...props} tokenUnit='K' />)
    const selected = screen.getByRole('button', { name: '/1K' })
    expect(selected).toHaveAttribute('aria-pressed', 'true')
    await user.click(selected)
    expect(selected).toHaveAttribute('aria-pressed', 'true')
    expect(props.onTokenUnitChange).toHaveBeenCalledTimes(1)
  })

  it('changes the recharge display mode with an accessible selected state', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    const { rerender } = render(<PricingToolbar {...props} />)
    await user.click(screen.getByRole('button', { name: 'Recharge' }))
    expect(props.onRechargePriceChange).toHaveBeenCalledWith(true)
    rerender(<PricingToolbar {...props} showRechargePrice />)
    expect(screen.getByRole('button', { name: 'Recharge' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
    expect(screen.getByRole('button', { name: 'Standard' })).toHaveAttribute(
      'aria-pressed',
      'false'
    )
  })

  it('switches to table view with the keyboard and exposes the selected view', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    const { rerender } = render(<PricingToolbar {...props} />)
    const tableButton = screen.getByRole('button', { name: 'Table view' })
    tableButton.focus()
    await user.keyboard('{Enter}')
    expect(props.onViewModeChange).toHaveBeenCalledWith('table')
    rerender(<PricingToolbar {...props} viewMode='table' />)
    expect(tableButton).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Card view' })).toHaveAttribute(
      'aria-pressed',
      'false'
    )
  })

  it('selects price sorting from the shared dropdown', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    render(<PricingToolbar {...props} />)
    await user.click(screen.getByRole('button', { name: 'Name' }))
    await user.click(
      screen.getByRole('menuitem', { name: 'Input Price: Low to High' })
    )
    expect(props.onSortChange).toHaveBeenCalledWith('input-price-low')
  })

  it('opens mobile filters from the left, selects a group, and restores focus on close', async () => {
    const props = toolbarProps()
    const user = userEvent.setup()
    const { rerender } = render(<PricingToolbar {...props} />)
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    const dialog = await screen.findByRole('dialog', { name: 'Filter' })
    expect(dialog).toHaveAttribute('data-side', 'left')
    expect(within(dialog).getByRole('button', { name: 'Reset' })).toBeDisabled()
    await user.click(within(dialog).getByRole('button', { name: /premium/ }))
    expect(props.onGroupChange).toHaveBeenCalledWith('premium')
    rerender(
      <PricingToolbar
        {...props}
        groupFilter='premium'
        hasActiveFilters
        activeFilterCount={1}
      />
    )
    expect(
      within(dialog).getByRole('button', { name: /premium/ })
    ).toHaveAttribute('aria-pressed', 'true')
    await user.click(within(dialog).getByRole('button', { name: 'Reset' }))
    expect(props.onClearFilters).toHaveBeenCalledOnce()
    await user.keyboard('{Escape}')
    expect(await screen.findByRole('button', { name: /Filter/ })).toHaveFocus()
  })
})
