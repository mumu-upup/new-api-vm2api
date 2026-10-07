import { useTranslation } from 'react-i18next'

import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { ModelTestResult as ModelTestResultData } from '../api'
import { getStatusCheckErrorMessage } from '../lib/status-check-error'

export function ModelTestResult(props: { result: ModelTestResultData }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const result = props.result
  let label = t('Skipped')
  let variant: StatusVariant = 'neutral'
  if (result.status === 'success') {
    label = t('Model responded')
    variant = 'success'
  } else if (result.status === 'failed') {
    label = t('Failed')
    variant = 'danger'
  } else if (result.status === 'needs_attention') {
    label = t('Needs attention')
    variant = 'warning'
  }
  let evaluation = t('Not scored')
  let evaluationVariant: StatusVariant = 'neutral'
  if (result.evaluation === 'matched') {
    evaluation = t('Answer matched')
    evaluationVariant = 'success'
  } else if (result.evaluation === 'mismatch') {
    evaluation = t('Answer differs')
    evaluationVariant = 'warning'
  }

  return (
    <div className='max-w-xl min-w-64 space-y-2 whitespace-normal'>
      <div className='flex flex-wrap gap-2'>
        <StatusBadge copyable={false} label={label} variant={variant} />
        {result.status === 'success' && (
          <StatusBadge
            copyable={false}
            label={evaluation}
            variant={evaluationVariant}
          />
        )}
      </div>
      {result.error_code && (
        <p className='text-muted-foreground text-xs'>
          {getStatusCheckErrorMessage(result.error_code, t)}
        </p>
      )}
      <dl className='grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-1 text-xs'>
        <dt className='text-muted-foreground'>{t('Requested model')}</dt>
        <dd className='wrap-anywhere'>{result.model}</dd>
        <dt className='text-muted-foreground'>{t('Reported model')}</dt>
        <dd className='wrap-anywhere'>
          {result.actual_model || t('Not reported')}
        </dd>
        <dt className='text-muted-foreground'>{t('Model response time')}</dt>
        <dd>
          {t('{{duration}} ms', {
            duration: formatNumber(result.elapsed_ms, locale),
          })}
        </dd>
      </dl>
      {result.answer && (
        <pre
          tabIndex={0}
          aria-label={t('Model answer')}
          className='bg-muted max-h-64 overflow-auto rounded-md p-2 text-xs wrap-anywhere whitespace-pre-wrap'
        >
          {result.answer}
        </pre>
      )}
    </div>
  )
}
