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
import { z } from 'zod'

const catalogSchema = z.object({
  version: z.literal(1),
  vendors: z.array(
    z.object({
      vendor_id: z.number().int().positive(),
      region: z.enum(['domestic', 'international']),
      groups: z.array(z.string().min(1)),
    })
  ),
})
export type MarketplaceVendorEntry = z.infer<
  typeof catalogSchema
>['vendors'][number]

export function parseMarketplaceCatalog(
  raw: string
): z.infer<typeof catalogSchema> {
  return catalogSchema.parse(JSON.parse(raw))
}

export function validateMarketplaceCatalog(
  rows: MarketplaceVendorEntry[],
  vendors: number[],
  groups: string[]
): string | null {
  if (rows.some((row) => !vendors.includes(row.vendor_id))) {
    return 'Select an existing vendor for every catalog entry'
  }
  if (new Set(rows.map((row) => row.vendor_id)).size !== rows.length) {
    return 'Each vendor can appear only once in the catalog'
  }
  if (
    rows.some(
      (row) =>
        row.groups.length === 0 ||
        new Set(row.groups).size !== row.groups.length ||
        row.groups.some((group) => !groups.includes(group))
    )
  ) {
    return 'Select existing billing groups for every catalog entry'
  }
  return null
}
