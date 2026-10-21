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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Form } from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { getVendors } from '@/features/models/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  parseMarketplaceCatalog,
  validateMarketplaceCatalog,
  type MarketplaceVendorEntry,
} from './marketplace-catalog'

type Props = { catalog: string; groupRatio: string }

export function MarketplaceSettingsSection(props: Props) {
  const { t } = useTranslation()
  const vendors = useQuery({
    queryKey: ['vendors', 'marketplace-catalog'],
    queryFn: async () => {
      const firstPage = requireServerSuccess(
        await getVendors({ p: 1, page_size: 100 })
      )
      if (!firstPage.data) return firstPage
      const items = new Map(
        firstPage.data.items.map((vendor) => [vendor.id, vendor])
      )
      for (let page = 2; items.size < firstPage.data.total; page++) {
        const nextPage = requireServerSuccess(
          await getVendors({ p: page, page_size: 100 })
        )
        const previousSize = items.size
        for (const vendor of nextPage.data?.items ?? []) {
          items.set(vendor.id, vendor)
        }
        if (items.size === previousSize) break
      }
      return {
        ...firstPage,
        data: { ...firstPage.data, items: [...items.values()] },
      }
    },
  })
  const parsed = useMemo(() => {
    try {
      const catalog = parseMarketplaceCatalog(props.catalog)
      const groups: unknown = JSON.parse(props.groupRatio || '{}')
      if (!groups || typeof groups !== 'object' || Array.isArray(groups)) {
        return null
      }
      return { catalog, groups: Object.keys(groups) }
    } catch {
      return null
    }
  }, [props.catalog, props.groupRatio])
  if (vendors.isPending) return <LoadingState />
  if (vendors.isError) {
    return <ErrorState onRetry={() => void vendors.refetch()} />
  }
  if (!parsed) {
    return (
      <ErrorState
        description={t(
          'The saved marketplace catalog or billing groups are invalid. Correct the saved configuration before editing.'
        )}
      />
    )
  }
  const items = vendors.data.data?.items ?? []
  if ((vendors.data.data?.total ?? 0) > items.length) {
    return (
      <ErrorState
        description={t(
          'The vendor list is incomplete. Reduce the vendor list before editing the catalog.'
        )}
      />
    )
  }
  return (
    <MarketplaceCatalogForm
      rows={parsed.catalog.vendors}
      groups={parsed.groups}
      vendors={items}
    />
  )
}

type EditorProps = {
  rows: MarketplaceVendorEntry[]
  groups: string[]
  vendors: { id: number; name: string }[]
}
function MarketplaceCatalogForm(props: EditorProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const queryClient = useQueryClient()
  const defaults = useMemo(
    () => ({ catalog: JSON.stringify(props.rows) }),
    [props.rows]
  )
  const settings = useSettingsForm({
    defaultValues: defaults,
    onSubmit: async (data) => {
      const error = validateMarketplaceCatalog(
        JSON.parse(data.catalog) as MarketplaceVendorEntry[],
        props.vendors.map((vendor) => vendor.id),
        props.groups
      )
      if (error) {
        settings.form.setError('root', { message: t(error) })
        throw new Error(error)
      }
      await updateOption.mutateAsync({
        key: 'marketplace_setting.catalog',
        value: JSON.stringify({
          version: 1,
          vendors: JSON.parse(data.catalog) as MarketplaceVendorEntry[],
        }),
      })
      await queryClient.invalidateQueries({ queryKey: ['pricing'] })
    },
  })
  const rows = JSON.parse(
    settings.form.watch('catalog')
  ) as MarketplaceVendorEntry[]
  const validation = validateMarketplaceCatalog(
    rows,
    props.vendors.map((vendor) => vendor.id),
    props.groups
  )
  const changeRows = (next: MarketplaceVendorEntry[]) =>
    settings.form.setValue('catalog', JSON.stringify(next), {
      shouldDirty: true,
    })
  return (
    <SettingsSection title={t('Model Marketplace')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Organize saved vendors and billing groups. Empty groups remain visible as pending connections.'
        )}
      </p>
      <div className='flex flex-wrap gap-4 text-sm underline'>
        <Link to='/models'>{t('Manage vendors')}</Link>
        <Link
          to='/system-settings/billing/$section'
          params={{ section: 'group-pricing' }}
        >
          {t('Manage billing groups')}
        </Link>
      </div>
      <FormNavigationGuard when={settings.isDirty} />
      <Form {...settings.form}>
        <SettingsForm onSubmit={settings.handleSubmit}>
          <SettingsPageFormActions
            onSave={settings.handleSubmit}
            onReset={settings.handleReset}
            isSaving={settings.isSubmitting}
            isSaveDisabled={Boolean(validation)}
          />
          {validation && (
            <p role='alert' className='text-destructive text-sm'>
              {t(validation)}
            </p>
          )}
          {rows.map((row, index) => (
            <div
              key={row.vendor_id}
              className='grid min-w-0 gap-4 rounded-lg border p-4 md:grid-cols-3'
            >
              <div className='space-y-2'>
                <Label htmlFor={`catalog-vendor-${index}`}>{t('Vendor')}</Label>
                <Combobox
                  id={`catalog-vendor-${index}`}
                  options={props.vendors.map((vendor) => ({
                    value: String(vendor.id),
                    label: vendor.name,
                  }))}
                  value={row.vendor_id ? String(row.vendor_id) : ''}
                  placeholder={t('Select vendor')}
                  onValueChange={(value) =>
                    changeRows(
                      rows.map((entry, i) =>
                        i === index
                          ? { ...entry, vendor_id: Number(value) }
                          : entry
                      )
                    )
                  }
                />
                {row.vendor_id > 0 &&
                  !props.vendors.some(
                    (vendor) => vendor.id === row.vendor_id
                  ) && (
                    <p role='alert'>
                      {t('Missing vendor ID: {{id}}', { id: row.vendor_id })}
                    </p>
                  )}
              </div>
              <div className='space-y-2'>
                <Label htmlFor={`catalog-region-${index}`}>{t('Region')}</Label>
                <Combobox
                  id={`catalog-region-${index}`}
                  options={[
                    { value: 'domestic', label: t('Domestic') },
                    { value: 'international', label: t('International') },
                  ]}
                  value={row.region}
                  onValueChange={(value) => {
                    if (value === 'domestic' || value === 'international') {
                      changeRows(
                        rows.map((entry, i) =>
                          i === index ? { ...entry, region: value } : entry
                        )
                      )
                    }
                  }}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor={`catalog-groups-${index}`}>
                  {t('Billing groups')}
                </Label>
                <MultiSelect
                  id={`catalog-groups-${index}`}
                  options={props.groups.map((group) => ({
                    value: group,
                    label: group,
                  }))}
                  selected={row.groups}
                  onChange={(groups) =>
                    changeRows(
                      rows.map((entry, i) =>
                        i === index ? { ...entry, groups } : entry
                      )
                    )
                  }
                />
                {row.groups
                  .filter((group) => !props.groups.includes(group))
                  .map((group) => (
                    <p key={group} role='alert'>
                      {t('Missing billing group: {{group}}', { group })}
                    </p>
                  ))}
              </div>
              <Button
                type='button'
                variant='outline'
                onClick={() => changeRows(rows.filter((_, i) => i !== index))}
              >
                {t('Remove vendor')}
              </Button>
            </div>
          ))}
          <Button
            type='button'
            variant='outline'
            disabled={rows.some((row) => row.vendor_id === 0)}
            onClick={() =>
              changeRows([
                ...rows,
                { vendor_id: 0, region: 'domestic', groups: [] },
              ])
            }
          >
            {t('Add vendor')}
          </Button>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
