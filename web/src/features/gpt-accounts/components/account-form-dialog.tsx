import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2 } from 'lucide-react'
import { useState } from 'react'
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
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { saveGptAccount } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import {
  accountFormSchema,
  accountImportSchema,
  type AccountFormValues,
} from '../lib/account-form'
import type { GptAccount, GptAccountGateway } from '../types'

interface AccountFormDialogProps {
  defaultGroup?: string
  defaultModels?: string
  account?: GptAccount
  gateways: GptAccountGateway[]
  gatewaysLoading: boolean
  gatewaysError: boolean
  onRetryGateways: () => void
  onClose: () => void
  onSaved: () => void
}

export function AccountFormDialog(props: AccountFormDialogProps) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const [entryMode, setEntryMode] = useState('import')
  const [accountLine, setAccountLine] = useState('')
  const [importError, setImportError] = useState(false)
  const root = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  const form = useForm<AccountFormValues>({
    resolver: zodResolver(accountFormSchema),
    defaultValues: {
      name: props.account?.name ?? '',
      email: props.account?.email ?? '',
      password: '',
      totp_secret: '',
      totp_provider: props.account
        ? props.account.totp_provider || 'local'
        : '2fa_cn',
      auto_relogin: props.account?.auto_relogin ?? true,
      group: props.account?.group ?? '',
      models: props.account?.models ?? '',
      channel_id: props.account?.channel_id ?? 0,
      gateway_id: props.account?.gateway_id ?? 0,
    },
  })
  const gatewayId = form.watch('gateway_id')
  const submit = async (values: AccountFormValues) => {
    setAccountLine('')
    form.resetField('password')
    form.resetField('totp_secret')
    const result = await operation.execute((signal) =>
      saveGptAccount(
        props.account?.id,
        {
          ...values,
          ...(root && !values.gateway_id
            ? { channel_id: values.channel_id || undefined }
            : { channel_id: undefined }),
        },
        signal
      )
    )
    if (!result) return
    form.reset()
    props.onSaved()
    props.onClose()
  }

  const submitForm = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!props.account && entryMode === 'import') {
      const result = accountImportSchema.safeParse(accountLine)
      if (!result.success) {
        setImportError(true)
        return
      }
      const values = form.getValues()
      form.reset(
        {
          ...values,
          ...result.data,
          name: values.name.trim()
            ? values.name
            : result.data.email.slice(0, 128),
        },
        { keepDefaultValues: true }
      )
      setAccountLine('')
      setImportError(false)
      setEntryMode('manual')
    }
    void form.handleSubmit(submit)(event)
  }

  const credentialFields = (
    <>
      <FormField
        control={form.control}
        name='email'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Email')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                type='email'
                disabled={operation.pending}
                autoComplete='off'
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Account password')}</FormLabel>
              <FormControl>
                <PasswordInput
                  {...field}
                  disabled={operation.pending}
                  autoComplete='new-password'
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='totp_secret'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('TOTP secret')}</FormLabel>
              <FormControl>
                <PasswordInput
                  {...field}
                  disabled={operation.pending}
                  autoComplete='new-password'
                  spellCheck={false}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          props.account
            ? 'Leave credential fields blank to keep the stored values.'
            : 'Enter credentials only in these fields. Leave them blank to use browser sign-in.'
        )}
      </p>
    </>
  )

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !operation.pending) props.onClose()
      }}
      title={t(props.account ? 'Edit GPT account' : 'Add GPT account')}
      description={t(
        'Store account credentials securely and authorize a Codex channel.'
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
            form='gpt-account-form'
            disabled={operation.pending}
          >
            {operation.pending && <Loader2 className='animate-spin' />}
            {t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form id='gpt-account-form' onSubmit={submitForm} className='space-y-4'>
          {operation.error && (
            <p role='alert' className='text-destructive text-sm'>
              {t(operation.error)}
            </p>
          )}
          <FormField
            control={form.control}
            name='gateway_id'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Account destination')}</FormLabel>
                <FormControl>
                  <NativeSelect
                    value={field.value}
                    onChange={(event) =>
                      field.onChange(Number(event.target.value))
                    }
                    disabled={
                      Boolean(props.account) ||
                      operation.pending ||
                      props.gatewaysLoading ||
                      props.gatewaysError
                    }
                    className='w-full'
                  >
                    <NativeSelectOption value={0}>
                      {t('Direct Codex channel')}
                    </NativeSelectOption>
                    {props.gateways.map((gateway) => (
                      <NativeSelectOption key={gateway.id} value={gateway.id}>
                        {gateway.name} ·{' '}
                        {gateway.provider === 'cpa'
                          ? 'CPA'
                          : gateway.provider === 'sub2api'
                            ? 'Sub2API'
                            : t('VM2API')}
                      </NativeSelectOption>
                    ))}
                    {props.account?.gateway_id &&
                      !props.gateways.some(
                        (gateway) => gateway.id === props.account?.gateway_id
                      ) && (
                        <NativeSelectOption value={props.account.gateway_id}>
                          {props.account.gateway_name ||
                            `#${props.account.gateway_id}`}
                        </NativeSelectOption>
                      )}
                  </NativeSelect>
                </FormControl>
                {props.gatewaysError && (
                  <p role='alert' className='text-destructive text-sm'>
                    {t('Gateway options could not be loaded.')}{' '}
                    <Button
                      type='button'
                      variant='link'
                      onClick={props.onRetryGateways}
                    >
                      {t('Retry')}
                    </Button>
                  </p>
                )}
                {gatewayId > 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Accounts added to this gateway share its channel. Requests may use any available account in the gateway.'
                    )}
                  </p>
                )}
                <FormMessage />
              </FormItem>
            )}
          />
          {!props.account && (
            <Tabs
              value={entryMode}
              onValueChange={(value) => {
                setEntryMode(String(value))
                form.setValue(
                  'totp_provider',
                  value === 'import' ? '2fa_cn' : 'local'
                )
                setAccountLine('')
                setImportError(false)
              }}
            >
              <TabsList>
                <TabsTrigger value='import' disabled={operation.pending}>
                  {t('Paste account')}
                </TabsTrigger>
                <TabsTrigger value='manual' disabled={operation.pending}>
                  {t('Enter separately')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value='import' className='space-y-3'>
                <Label htmlFor='gpt-account-line'>{t('Account line')}</Label>
                <PasswordInput
                  id='gpt-account-line'
                  value={accountLine}
                  onChange={(event) => {
                    setAccountLine(event.target.value)
                    setImportError(false)
                  }}
                  onPaste={(event) => {
                    if (/[\r\n]/.test(event.clipboardData.getData('text'))) {
                      event.preventDefault()
                      setAccountLine('')
                      setImportError(true)
                    }
                  }}
                  disabled={operation.pending}
                  autoComplete='new-password'
                  spellCheck={false}
                  aria-invalid={importError}
                  aria-describedby='gpt-account-line-help'
                  aria-errormessage={
                    importError ? 'gpt-account-line-error' : undefined
                  }
                />
                <p
                  id='gpt-account-line-help'
                  className='text-muted-foreground text-sm'
                >
                  {t(
                    'Paste one line in email----password----TOTP secret format.'
                  )}
                </p>
                {importError && (
                  <p
                    id='gpt-account-line-error'
                    role='alert'
                    className='text-destructive text-sm'
                  >
                    {t(
                      'Enter one account as email----password----TOTP secret.'
                    )}
                  </p>
                )}
              </TabsContent>
              <TabsContent value='manual' className='space-y-4'>
                {credentialFields}
              </TabsContent>
            </Tabs>
          )}
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    disabled={operation.pending}
                    autoComplete='off'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          {props.account && credentialFields}
          <FormField
            control={form.control}
            name='totp_provider'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Sign-in code source')}</FormLabel>
                <FormControl>
                  <RadioGroup
                    aria-label={t('Sign-in code source')}
                    value={field.value}
                    onValueChange={field.onChange}
                    disabled={operation.pending}
                    aria-describedby={
                      field.value === '2fa_cn'
                        ? 'gpt-account-code-source-help'
                        : undefined
                    }
                    className='flex flex-wrap gap-4'
                  >
                    <div className='flex items-center gap-2'>
                      <RadioGroupItem
                        id='gpt-account-code-local'
                        value='local'
                      />
                      <Label htmlFor='gpt-account-code-local'>
                        {t('Generate locally')}
                      </Label>
                    </div>
                    <div className='flex items-center gap-2'>
                      <RadioGroupItem
                        id='gpt-account-code-2fa-cn'
                        value='2fa_cn'
                      />
                      <Label htmlFor='gpt-account-code-2fa-cn'>2fa.cn</Label>
                    </div>
                  </RadioGroup>
                </FormControl>
                {field.value === '2fa_cn' && (
                  <p
                    id='gpt-account-code-source-help'
                    className='text-muted-foreground text-sm'
                  >
                    {t(
                      '2fa.cn receives this account’s TOTP secret to generate sign-in codes.'
                    )}
                  </p>
                )}
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='auto_relogin'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between'>
                <FormLabel>
                  {t('Automatically sign in again after an upstream 401')}
                </FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={operation.pending}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          {gatewayId === 0 && (
            <>
              <div className='grid gap-4 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='group'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Group')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          disabled={operation.pending}
                          readOnly={Boolean(props.account?.channel_id)}
                          placeholder={t('Use global default: {{group}}', {
                            group: props.defaultGroup ?? 'default',
                          })}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='models'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Models')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          disabled={operation.pending}
                          readOnly={Boolean(props.account?.channel_id)}
                          placeholder={t(
                            'Use global default models: {{models}}',
                            { models: props.defaultModels ?? 'gpt-5.6' }
                          )}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
              <p className='text-muted-foreground text-sm'>
                {props.account?.channel_id
                  ? t('Manage routing settings in the associated channel.')
                  : t(
                      'Separate model names with commas. Leave blank to use the global default models.'
                    )}
              </p>
              {root && (
                <FormField
                  control={form.control}
                  name='channel_id'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Existing Codex channel ID')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={0}
                          onChange={(event) =>
                            field.onChange(Number(event.target.value))
                          }
                          disabled={operation.pending}
                        />
                      </FormControl>
                      <FormMessage />
                      <p className='text-muted-foreground text-sm'>
                        {t(
                          'Use 0 to create a channel after authorization. Existing channels must use one Codex credential and match this account.'
                        )}
                      </p>
                    </FormItem>
                  )}
                />
              )}
            </>
          )}
        </form>
      </Form>
    </Dialog>
  )
}
