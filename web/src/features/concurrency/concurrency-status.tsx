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
import { useContext } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { TokenConcurrencyContext } from './use-request-concurrency'

export function TokenConcurrencyStatus(props: { tokenId: number }) {
  const concurrency = useContext(TokenConcurrencyContext)
  return <ConcurrencyStatus current={concurrency?.tokens?.[props.tokenId]} />
}

export function ConcurrencyStatus(props: {
  current?: number
  limit?: number
  label?: string
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const current = props.current
  let value =
    current === undefined ? t('Unknown') : formatNumber(current, locale)
  if (props.limit !== undefined) {
    const limit =
      props.limit === 0 ? t('Unlimited') : formatNumber(props.limit, locale)
    value = `${value} / ${limit}`
  }
  const atLimit =
    current !== undefined && !!props.limit && current >= props.limit

  return (
    <StatusBadge
      label={value}
      aria-label={`${props.label ?? t('Concurrent requests')}: ${value}`}
      variant={atLimit ? 'warning' : 'neutral'}
      copyable={false}
      className='tabular-nums'
    />
  )
}
