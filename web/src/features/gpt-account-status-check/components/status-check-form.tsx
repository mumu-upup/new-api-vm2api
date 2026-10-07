import { zodResolver } from '@hookform/resolvers/zod'
import { Controller, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'

import type { ModelTestOptions } from '../api'
import {
  statusCheckFormSchema,
  type StatusCheckFormValues,
} from '../lib/parser'
import { ModelTestFields } from './model-test-fields'

type StatusCheckFormProps = {
  disabled?: boolean
  onSubmit: (credentials: string, modelTest?: ModelTestOptions) => Promise<void>
}

export function StatusCheckForm(props: StatusCheckFormProps) {
  const { t } = useTranslation()
  const form = useForm<StatusCheckFormValues>({
    defaultValues: {
      credentials: '',
      mode: 'model',
      model: 'gpt-6.1-sol',
      prompt: t(
        'A box holds 12 red balls and 8 blue balls. You remove 5 red balls and add 3 blue balls. How many balls are now in the box? Reply with only the integer.'
      ),
      expected_answer: '18',
    },
    resolver: zodResolver(statusCheckFormSchema),
  })
  const error = form.formState.errors.credentials

  const submit = async (values: StatusCheckFormValues) => {
    form.reset({ ...values, credentials: '' })
    const modelTest =
      values.mode === 'model'
        ? {
            model: values.model,
            prompt: values.prompt,
            expected_answer: values.expected_answer,
          }
        : undefined
    await props.onSubmit(values.credentials, modelTest)
  }

  return (
    <Card className='min-w-0'>
      <CardHeader>
        <CardTitle>{t('Check GPT account status')}</CardTitle>
        <CardDescription>
          {t(
            'Credentials are used once and are not saved locally. Model testing temporarily authorizes this account and consumes its model quota.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className='space-y-4' onSubmit={form.handleSubmit(submit)}>
          <Controller
            control={form.control}
            name='mode'
            render={({ field }) => (
              <RadioGroup
                aria-label={t('Check mode')}
                value={field.value}
                onValueChange={field.onChange}
                disabled={props.disabled}
                className='flex flex-wrap gap-4'
              >
                <div className='flex items-center gap-2'>
                  <RadioGroupItem id='status-check-login-only' value='login' />
                  <Label htmlFor='status-check-login-only'>
                    {t('Sign-in only')}
                  </Label>
                </div>
                <div className='flex items-center gap-2'>
                  <RadioGroupItem id='status-check-model' value='model' />
                  <Label htmlFor='status-check-model'>
                    {t('Sign-in and model answer')}
                  </Label>
                </div>
              </RadioGroup>
            )}
          />
          {form.watch('mode') === 'model' && (
            <ModelTestFields form={form} disabled={props.disabled} />
          )}
          <div className='space-y-2'>
            <Label htmlFor='status-check-credentials'>{t('Credentials')}</Label>
            <Textarea
              id='status-check-credentials'
              className='min-h-48'
              aria-describedby={
                error
                  ? 'status-check-help status-check-error'
                  : 'status-check-help'
              }
              aria-invalid={Boolean(error)}
              autoComplete='off'
              disabled={props.disabled}
              placeholder={t('email----password----totp secret')}
              rows={8}
              spellCheck={false}
              {...form.register('credentials')}
            />
            <p id='status-check-help' className='text-muted-foreground text-xs'>
              {t('One account per line: email----password----TOTP secret')}
            </p>
            {error && (
              <p
                id='status-check-error'
                role='alert'
                className='text-destructive text-sm'
              >
                {t(
                  'Check the account format, remove duplicates, and use at most 100 rows.'
                )}
              </p>
            )}
          </div>
          <Button
            disabled={props.disabled || !form.watch('credentials').trim()}
            type='submit'
          >
            {t('Check status')}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
