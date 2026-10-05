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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
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
import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import {
  userConcurrencySchema,
  type UserConcurrencyValues,
} from '../lib/user-concurrency'

export function UserConcurrencyDialog(props: {
  userId: number
  username: string
  limit: number
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const formId = useId()
  const client = useQueryClient()
  const form = useForm<UserConcurrencyValues>({
    resolver: zodResolver(userConcurrencySchema),
    defaultValues: { max_concurrent_requests: props.limit },
  })
  const mutation = useMutation({
    mutationFn: async (values: UserConcurrencyValues) => {
      const response = await api.put(
        `/api/user/${props.userId}/concurrency`,
        values
      )
      return requireServerSuccess(response.data)
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['request-concurrency'] })
      void client.invalidateQueries({ queryKey: ['users'] })
      toast.success(t('Concurrency limit updated'))
      props.onOpenChange(false)
    },
    onError: (error) => {
      form.setError('root', {
        message: getServerErrorMessage(
          error,
          t('Failed to update concurrency limit')
        ),
      })
    },
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => !mutation.isPending && props.onOpenChange(open)}
      title={t('Edit concurrency limit')}
      description={t(
        'Set the shared limit across all API keys for {{username}}. 0 means unlimited.',
        { username: props.username }
      )}
      contentClassName='sm:max-w-md'
      footer={
        <>
          <Button
            variant='outline'
            disabled={mutation.isPending}
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={formId} disabled={mutation.isPending}>
            {mutation.isPending ? t('Saving...') : t('Save changes')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={formId}
          noValidate
          onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='max_concurrent_requests'
            render={({ field, fieldState }) => (
              <FormItem>
                <FormLabel>{t('Concurrent request limit')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    min={0}
                    max={2147483647}
                    step={1}
                    value={Number.isNaN(field.value) ? '' : field.value}
                    onChange={(event) =>
                      field.onChange(event.target.valueAsNumber)
                    }
                    disabled={mutation.isPending}
                  />
                </FormControl>
                {fieldState.error && (
                  <FormMessage role='alert'>
                    {t('Enter a whole number from 0 to 2147483647')}
                  </FormMessage>
                )}
              </FormItem>
            )}
          />
          {form.formState.errors.root && (
            <p role='alert' className='text-destructive text-sm'>
              {form.formState.errors.root.message}
            </p>
          )}
        </form>
      </Form>
    </Dialog>
  )
}
