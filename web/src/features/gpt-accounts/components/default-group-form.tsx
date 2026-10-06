import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { SettingsControlGroup } from '@/features/system-settings/components/settings-form-layout'
import { AuthOperationError } from '@/lib/secure-verification'

import { updateGptAccountConfig } from '../api'
import { defaultGroupSchema } from '../lib/group-form'

export function DefaultGroupForm(props: {
  defaultGroup: string
  canEdit: boolean
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const form = useForm({
    resolver: zodResolver(defaultGroupSchema),
    defaultValues: { default_group: props.defaultGroup },
  })
  const mutation = useMutation({
    mutationFn: updateGptAccountConfig,
    onSuccess: (data) => {
      form.reset({ default_group: data.default_group })
      toast.success(t('Saved successfully'))
      props.onSaved()
    },
    onError: (error) => {
      form.setError('root', {
        message:
          error instanceof AuthOperationError
            ? error.message
            : 'Operation failed. Please try again.',
      })
    },
  })
  return (
    <SettingsControlGroup>
      <Form {...form}>
        <form
          onSubmit={form.handleSubmit((values) =>
            mutation.mutate(values.default_group)
          )}
          className='flex flex-wrap items-start gap-3'
        >
          <FormField
            control={form.control}
            name='default_group'
            render={({ field }) => (
              <FormItem className='w-full sm:w-64'>
                <FormLabel>{t('Global default group')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    readOnly={!props.canEdit}
                    disabled={mutation.isPending}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          {props.canEdit && (
            <Button
              type='submit'
              className='sm:mt-6'
              disabled={mutation.isPending || !form.formState.isDirty}
            >
              {t('Save default group')}
            </Button>
          )}
          <p className='text-muted-foreground w-full text-sm'>
            {t(
              'Applies to new accounts and imports when no group is specified.'
            )}
          </p>
          {form.formState.errors.root && (
            <p role='alert' className='text-destructive text-sm'>
              {t(form.formState.errors.root.message ?? '')}
            </p>
          )}
        </form>
      </Form>
    </SettingsControlGroup>
  )
}
