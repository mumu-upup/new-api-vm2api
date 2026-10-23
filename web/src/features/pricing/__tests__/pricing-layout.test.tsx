/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { EmptyState } from '../components/empty-state'
import { PricingScrollContainer } from '../components/pricing-scroll-container'

describe('pricing page layout', () => {
  it('shows a deliberate pending state for an empty catalog group', () => {
    render(
      <EmptyState
        pendingConnection
        hasActiveFilters
        onClearFilters={() => {}}
      />
    )
    expect(screen.getByText('Pending connection')).toBeVisible()
    expect(
      screen.getByText(
        'This group is configured but has no connected models yet.'
      )
    ).toBeVisible()
    expect(screen.queryByText('No models found')).toBeNull()
  })

  it('provides a bounded vertical scroll container for long model lists', () => {
    const { container } = render(
      <PricingScrollContainer>
        <div>model cards</div>
      </PricingScrollContainer>
    )

    expect(container.firstElementChild).toHaveClass(
      'min-h-0',
      'flex-1',
      'overflow-y-auto'
    )
  })
})
