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
  parseMarketplaceCatalog,
  validateMarketplaceCatalog,
} from '../marketplace-catalog'

describe('marketplace catalog editor', () => {
  it('keeps invalid persisted JSON unavailable instead of replacing it with an empty catalog', () => {
    expect(() => parseMarketplaceCatalog('{broken')).toThrow()
    expect(() =>
      parseMarketplaceCatalog('{"version":2,"vendors":[]}')
    ).toThrow()
  })
  it('preserves saved vendor IDs and pending groups when parsing a valid catalog', () => {
    const value = {
      version: 1,
      vendors: [
        { vendor_id: 8, region: 'international', groups: ['claude-code'] },
      ],
    }
    expect(parseMarketplaceCatalog(JSON.stringify(value))).toEqual(value)
  })
  it('rejects missing vendors, stale groups and duplicate vendor associations before saving', () => {
    const row = {
      vendor_id: 8,
      region: 'international' as const,
      groups: ['claude-code'],
    }
    expect(validateMarketplaceCatalog([row], [8], ['claude-code'])).toBeNull()
    expect(validateMarketplaceCatalog([row], [], ['claude-code'])).toBe(
      'Select an existing vendor for every catalog entry'
    )
    expect(validateMarketplaceCatalog([row], [8], [])).toBe(
      'Select existing billing groups for every catalog entry'
    )
    expect(validateMarketplaceCatalog([row, row], [8], ['claude-code'])).toBe(
      'Each vendor can appear only once in the catalog'
    )
  })
})
