import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
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
import { getGroups } from '@/features/users/api'
import { AuthOperationError } from '@/lib/secure-verification'
import { requireServerSuccess } from '@/lib/server-error-message'

import { updateGptAccountConfig } from '../api'
import { defaultGroupSchema } from '../lib/group-form'
import type { GptAccountGateway } from '../types'

export function DefaultGroupForm(props: {
  defaultGroup: string
  defaultModels: string
  canEdit: boolean
  onSaved: () => void
  defaultGatewayId?: number
  gateways?: GptAccountGateway[]
}) {
  const { t } = useTranslation()
  const groups = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
  })
  const groupOptions = (groups.data?.data ?? []).map((group) => ({
    value: group,
    label: group,
  }))
  const form = useForm({
    resolver: zodResolver(defaultGroupSchema),
    defaultValues: {
      default_group: props.defaultGroup,
      default_models: props.defaultModels,
      default_gateway_id: props.defaultGatewayId ? String(props.defaultGatewayId) : '',
    },
  })
  const mutation = useMutation({
    mutationFn: updateGptAccountConfig,
    onSuccess: (data) => {
      form.reset({
        default_group: data.default_group,
        default_models: data.default_models ?? props.defaultModels,
        default_gateway_id: data.default_gateway_id ? String(data.default_gateway_id) : '',
      })
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
            mutation.mutate({
              ...(form.formState.dirtyFields.default_group
                ? { default_group: values.default_group }
                : {}),
              ...(form.formState.dirtyFields.default_models
                ? { default_models: values.default_models }
                : {}),
              ...(form.formState.dirtyFields.default_gateway_id
                ? { default_gateway_id: values.default_gateway_id ? Number(values.default_gateway_id) : 0 }
                : {}),
            })
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
                  <Combobox
                    {...field}
                    options={groupOptions}
                    onValueChange={field.onChange}
                    allowCustomValue={false}
                    placeholder={t('Select a group')}
                    disabled={
                      !props.canEdit ||
                      mutation.isPending ||
                      groups.isPending ||
                      groups.isError ||
                      !groupOptions.length
                    }
                  />
                </FormControl>
                <FormMessage />
                {groups.isError && (
                  <p role='alert' className='text-destructive text-sm'>
                    {t('Failed to load')}
                  </p>
                )}
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='default_gateway_id'
            render={({ field }) => (
              <FormItem className='w-full sm:w-64'>
                <FormLabel>{t('Default VM2API gateway')}</FormLabel>
                <FormControl>
                  <Combobox
                    value={field.value ?? ''}
                    options={[
                      { value: '', label: t('Automatic') },
                      ...(props.gateways ?? [])
                        .filter((gateway) => gateway.provider === 'vm2api')
                        .map((gateway) => ({ value: String(gateway.id), label: gateway.name })),
                    ]}
                    onValueChange={field.onChange}
                    allowCustomValue={false}
                    disabled={!props.canEdit || mutation.isPending}
                    placeholder={t('Automatic')}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='default_models'
            render={({ field }) => (
              <FormItem className='w-full sm:min-w-64 sm:flex-1'>
                <FormLabel>{t('Global default models')}</FormLabel>
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
              {t('Save defaults')}
            </Button>
          )}
          <p className='text-muted-foreground w-full text-sm'>
            {t(
              'Applies to new accounts and imports when no group is specified.'
            )}{' '}
            {t(
              'Models apply when none are specified. Use GPT 5.6 or later, separated by commas.'
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
