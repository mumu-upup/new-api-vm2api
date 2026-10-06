import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2 } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { PasswordInput } from '@/components/password-input'
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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'

import { saveGptAccountGateway } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import { gatewayFormSchema, type GatewayFormValues } from '../lib/gateway-form'
import type { GptAccountGateway } from '../types'

export function GatewayFormDialog(props: {
  defaultGroup?: string
  gateway?: GptAccountGateway
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const form = useForm<GatewayFormValues>({
    resolver: zodResolver(gatewayFormSchema(Boolean(props.gateway))),
    defaultValues: {
      name: props.gateway?.name ?? '',
      provider: props.gateway?.provider ?? 'cpa',
      management_version: props.gateway?.management_version ?? 'v8',
      management_url: props.gateway?.management_url ?? '',
      relay_url: props.gateway?.relay_url ?? '',
      allow_local_http: props.gateway?.allow_local_http ?? false,
      remote_group_id: props.gateway?.remote_group_id ?? 0,
      group: props.gateway?.group ?? '',
      models: props.gateway?.models ?? 'gpt-5',
      management_key: '',
      relay_key: '',
    },
  })
  const provider = form.watch('provider')
  const submit = async (values: GatewayFormValues) => {
    form.resetField('management_key')
    form.resetField('relay_key')
    const saved = await operation.execute(
      props.gateway
        ? {
            scope: 'gpt_account_gateway.update',
            context: { gateway_id: props.gateway.id },
          }
        : {
            scope: 'gpt_account_gateway.create',
            context: { name: values.name },
          },
      (proof, signal) =>
        saveGptAccountGateway(props.gateway?.id, values, proof, signal)
    )
    if (!saved) return
    props.onSaved()
    props.onClose()
  }
  const textFields = [
    { name: 'name', label: t('Name'), immutable: false, secret: false },
    {
      name: 'management_url',
      label: t('Management URL'),
      immutable: true,
      secret: false,
    },
    {
      name: 'relay_url',
      label: t('Relay URL'),
      immutable: true,
      secret: false,
    },
    {
      name: 'management_key',
      label: t('Management key'),
      immutable: false,
      secret: true,
    },
    {
      name: 'relay_key',
      label: t('Relay key'),
      immutable: false,
      secret: true,
    },
    { name: 'group', label: t('Group'), immutable: false, secret: false },
    { name: 'models', label: t('Models'), immutable: false, secret: false },
  ] as const
  return (
    <>
      {!operation.verification.isActive && (
        <Dialog
          open={!operation.verification.isActive}
          onOpenChange={(open) => {
            if (!open && !operation.pending) props.onClose()
          }}
          title={props.gateway ? t('Edit gateway') : t('Add gateway')}
          description={t(
            'Configure a shared gateway channel for imported accounts.'
          )}
          footer={
            <>
              <Button
                variant='outline'
                onClick={props.onClose}
                disabled={operation.pending}
              >
                {t('Cancel')}
              </Button>
              <Button
                type='submit'
                form='gpt-gateway-form'
                disabled={operation.pending}
              >
                {operation.pending && <Loader2 className='animate-spin' />}
                {t('Save')}
              </Button>
            </>
          }
        >
          <Form {...form}>
            <form
              id='gpt-gateway-form'
              noValidate
              onSubmit={form.handleSubmit(submit)}
              className='space-y-4'
            >
              {operation.error && (
                <p role='alert' className='text-destructive text-sm'>
                  {operation.error}
                </p>
              )}
              <FormField
                control={form.control}
                name='provider'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Provider')}</FormLabel>
                    <FormControl>
                      <NativeSelect
                        value={field.value}
                        disabled={Boolean(props.gateway) || operation.pending}
                        onChange={(event) => {
                          field.onChange(event.target.value)
                          form.setValue(
                            'management_version',
                            event.target.value === 'cpa' ? 'v8' : ''
                          )
                        }}
                      >
                        <NativeSelectOption value='cpa'>CPA</NativeSelectOption>
                        <NativeSelectOption value='sub2api'>
                          Sub2API
                        </NativeSelectOption>
                      </NativeSelect>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {provider === 'cpa' && (
                <FormField
                  control={form.control}
                  name='management_version'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Management API version')}</FormLabel>
                      <FormControl>
                        <NativeSelect
                          {...field}
                          disabled={Boolean(props.gateway) || operation.pending}
                        >
                          <NativeSelectOption value='v8'>v8</NativeSelectOption>
                          <NativeSelectOption value='legacy'>
                            {t('Legacy')}
                          </NativeSelectOption>
                        </NativeSelect>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
              {textFields.map((entry) => (
                <FormField
                  key={entry.name}
                  control={form.control}
                  name={entry.name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{entry.label}</FormLabel>
                      <FormControl>
                        {entry.secret ? (
                          <PasswordInput
                            {...field}
                            disabled={operation.pending}
                            autoComplete='new-password'
                            spellCheck={false}
                          />
                        ) : (
                          <Input
                            {...field}
                            disabled={operation.pending}
                            readOnly={Boolean(props.gateway) && entry.immutable}
                            placeholder={
                              entry.name === 'group'
                                ? t('Use global default: {{group}}', {
                                    group: props.defaultGroup ?? 'default',
                                  })
                                : undefined
                            }
                            autoComplete='off'
                          />
                        )}
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
              {props.gateway && (
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Leave key fields blank to keep the stored keys. Connection settings cannot be changed after creation.'
                  )}
                </p>
              )}
              {provider === 'sub2api' && (
                <FormField
                  control={form.control}
                  name='remote_group_id'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Sub2API group ID')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={1}
                          readOnly={Boolean(props.gateway)}
                          disabled={operation.pending}
                          onChange={(event) =>
                            field.onChange(Number(event.target.value))
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
              <FormField
                control={form.control}
                name='allow_local_http'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3'>
                    <FormLabel>{t('Allow local HTTP gateway')}</FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={Boolean(props.gateway) || operation.pending}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Use HTTPS for remote gateways. Local HTTP is limited to loopback addresses.'
                )}
              </p>
            </form>
          </Form>
        </Dialog>
      )}
      <SecureVerificationDialog {...operation.verification.dialogProps} />
    </>
  )
}
