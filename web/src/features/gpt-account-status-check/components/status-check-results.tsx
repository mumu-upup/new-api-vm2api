import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { StatusCheckView } from '../api'
import { getStatusCheckErrorMessage } from '../lib/status-check-error'
import { ModelTestResult } from './model-test-result'

type StatusCheckResultsProps = {
  view: StatusCheckView
  onCancel: () => void
  onRetry: () => void
  error: boolean
  cancelling: boolean
}

function statusVariant(status: string): StatusVariant {
  if (status === 'success') return 'success'
  if (status === 'needs_attention') return 'warning'
  if (status === 'failed') return 'danger'
  if (status === 'cancelled') return 'neutral'
  return 'info'
}

export function StatusCheckResults(props: StatusCheckResultsProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const progress = props.view.total
    ? Math.round((props.view.completed / props.view.total) * 100)
    : 0
  const active =
    props.view.state === 'pending' || props.view.state === 'running'

  const statusLabel = (status: string) => {
    if (status === 'success') return t('Success')
    if (status === 'failed') return t('Failed')
    if (status === 'needs_attention') return t('Needs attention')
    if (status === 'cancelled') return t('Cancelled')
    if (status === 'running') return t('Checking')
    return t('Pending')
  }

  return (
    <Card className='min-w-0'>
      <CardHeader>
        <CardTitle>{t('Status check results')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-4'>
        {props.view.model_test && (
          <div className='space-y-2 text-xs'>
            <p className='text-muted-foreground'>
              {t(
                'A successful check means the model returned an answer. Answer matching is shown separately.'
              )}
            </p>
            <dl className='space-y-1'>
              <dt className='font-medium'>{t('Test question')}</dt>
              <dd className='max-h-32 overflow-auto wrap-anywhere whitespace-pre-wrap'>
                {props.view.model_test.prompt}
              </dd>
              <dt className='font-medium'>{t('Expected answer (optional)')}</dt>
              <dd className='max-h-24 overflow-auto wrap-anywhere whitespace-pre-wrap'>
                {props.view.model_test.expected_answer || t('Not scored')}
              </dd>
            </dl>
            <p className='text-muted-foreground'>
              {t(
                'Matching is case-sensitive after trimming whitespace and normalizing line endings. A single answer cannot establish hidden model downgrading.'
              )}
            </p>
          </div>
        )}
        <div className='space-y-2'>
          <div className='flex justify-between text-sm'>
            <span>{t('Progress')}</span>
            <span>
              {formatNumber(props.view.completed, locale)}/
              {formatNumber(props.view.total, locale)}
            </span>
          </div>
          <Progress aria-label={t('Progress')} value={progress} />
        </div>
        <div className='flex flex-wrap gap-2 text-sm'>
          <StatusBadge
            copyable={false}
            label={`${t('Success')}: ${formatNumber(props.view.success, locale)}`}
            variant='success'
          />
          <StatusBadge
            copyable={false}
            label={`${t('Failed')}: ${formatNumber(props.view.failed, locale)}`}
            variant='danger'
          />
          <StatusBadge
            copyable={false}
            label={`${t('Needs attention')}: ${formatNumber(props.view.needs_attention, locale)}`}
            variant='warning'
          />
          <StatusBadge
            copyable={false}
            label={`${t('Cancelled')}: ${formatNumber(props.view.cancelled, locale)}`}
            variant='neutral'
          />
        </div>
        {active && (
          <Button
            disabled={props.cancelling}
            variant='destructive'
            size='sm'
            onClick={props.onCancel}
            type='button'
          >
            {t('Cancel check')}
          </Button>
        )}
        {props.error && (
          <ErrorState
            className='min-h-0 py-4'
            description={t('The status check could not be refreshed.')}
            onRetry={props.onRetry}
            title={t('Status check unavailable')}
          />
        )}
        <StaticDataTable
          className='max-h-[32rem] overflow-auto'
          tableProps={{ 'aria-label': t('Status check results') }}
          data={props.view.results}
          columns={[
            { id: 'email', header: t('Email'), cell: (result) => result.email },
            {
              id: 'status',
              header: t('Status'),
              cell: (result) => (
                <div>
                  <StatusBadge
                    copyable={false}
                    label={statusLabel(result.status)}
                    variant={statusVariant(result.status)}
                  />
                  {result.error_code && (
                    <p className='text-muted-foreground mt-1 max-w-[28rem] text-xs whitespace-normal'>
                      {getStatusCheckErrorMessage(result.error_code, t)}
                    </p>
                  )}
                  {result.login_status && (
                    <p className='mt-1 text-xs'>
                      {t('Sign-in: {{status}}', {
                        status: statusLabel(result.login_status),
                      })}
                    </p>
                  )}
                  {result.duration_ms != null && (
                    <p className='text-muted-foreground mt-1 text-xs'>
                      {t('{{duration}} ms', {
                        duration: formatNumber(result.duration_ms, locale),
                      })}
                    </p>
                  )}
                </div>
              ),
            },
            ...(props.view.model_test
              ? [
                  {
                    id: 'model_test',
                    header: t('Model test'),
                    cell: (result: StatusCheckView['results'][number]) =>
                      result.model_test ? (
                        <ModelTestResult result={result.model_test} />
                      ) : (
                        <span>{statusLabel(result.status)}</span>
                      ),
                  },
                ]
              : []),
          ]}
        />
      </CardContent>
    </Card>
  )
}
