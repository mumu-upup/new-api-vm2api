import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import type { StatusCheckFormValues } from '../lib/parser'

type ModelTestFieldsProps = {
  form: UseFormReturn<StatusCheckFormValues>
  disabled?: boolean
}

export function ModelTestFields(props: ModelTestFieldsProps) {
  const { t } = useTranslation()
  const errors = props.form.formState.errors

  return (
    <FieldGroup>
      <Field
        data-invalid={Boolean(errors.model)}
        data-disabled={props.disabled}
      >
        <FieldLabel htmlFor='status-check-model-id'>
          {t('Requested model')}
        </FieldLabel>
        <Input
          id='status-check-model-id'
          autoComplete='off'
          spellCheck={false}
          disabled={props.disabled}
          aria-invalid={Boolean(errors.model)}
          aria-describedby='status-check-model-help'
          {...props.form.register('model')}
        />
        <p
          id='status-check-model-help'
          role={errors.model ? 'alert' : undefined}
          className={
            errors.model
              ? 'text-destructive text-xs'
              : 'text-muted-foreground text-xs'
          }
        >
          {errors.model
            ? t(
                'Use a model ID of 1–100 letters, digits, dots, underscores, colons, or hyphens, starting with a letter or digit.'
              )
            : t(
                'Enter a model ID available to this account. The suggested ID does not guarantee access.'
              )}
        </p>
      </Field>
      <Field
        data-invalid={Boolean(errors.prompt)}
        data-disabled={props.disabled}
      >
        <FieldLabel htmlFor='status-check-prompt'>
          {t('Test question')}
        </FieldLabel>
        <Textarea
          id='status-check-prompt'
          rows={3}
          disabled={props.disabled}
          aria-invalid={Boolean(errors.prompt)}
          aria-describedby='status-check-prompt-help'
          {...props.form.register('prompt')}
        />
        <p
          id='status-check-prompt-help'
          role={errors.prompt ? 'alert' : undefined}
          className={
            errors.prompt
              ? 'text-destructive text-xs'
              : 'text-muted-foreground text-xs'
          }
        >
          {t('Enter a question of 1–4,000 UTF-8 bytes.')}
        </p>
      </Field>
      <Field
        data-invalid={Boolean(errors.expected_answer)}
        data-disabled={props.disabled}
      >
        <FieldLabel htmlFor='status-check-expected'>
          {t('Expected answer (optional)')}
        </FieldLabel>
        <Textarea
          id='status-check-expected'
          rows={2}
          disabled={props.disabled}
          aria-invalid={Boolean(errors.expected_answer)}
          aria-describedby='status-check-expected-help status-check-matching-help'
          {...props.form.register('expected_answer')}
        />
        <p
          id='status-check-expected-help'
          role={errors.expected_answer ? 'alert' : undefined}
          className={
            errors.expected_answer
              ? 'text-destructive text-xs'
              : 'text-muted-foreground text-xs'
          }
        >
          {t('Up to 1,000 UTF-8 bytes. Leave empty to skip answer matching.')}
        </p>
        <p
          id='status-check-matching-help'
          className='text-muted-foreground text-xs'
        >
          {t(
            'Matching is case-sensitive after trimming whitespace and normalizing line endings. A single answer cannot establish hidden model downgrading.'
          )}
        </p>
      </Field>
    </FieldGroup>
  )
}
