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
import { Switch } from '@/components/ui/switch'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { saveGptAccount } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import { accountFormSchema, type AccountFormValues } from '../lib/account-form'
import type { GptAccount } from '../types'

interface AccountFormDialogProps {
  account?: GptAccount
  onClose: () => void
  onSaved: () => void
}

export function AccountFormDialog(props: AccountFormDialogProps) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
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
      auto_relogin: props.account?.auto_relogin ?? true,
      group: props.account?.group ?? 'default',
      models: props.account?.models ?? '',
      channel_id: props.account?.channel_id ?? 0,
    },
  })
  const submit = async (values: AccountFormValues) => {
    form.resetField('password')
    form.resetField('totp_secret')
    const result = await operation.execute(
      props.account
        ? {
            scope: 'gpt_account.update',
            context: { account_id: props.account.id },
          }
        : { scope: 'gpt_account.create', context: { email: values.email } },
      (proof, signal) =>
        saveGptAccount(
          props.account?.id,
          {
            ...values,
            ...(root
              ? { channel_id: values.channel_id || undefined }
              : { channel_id: undefined }),
          },
          proof,
          signal
        )
    )
    if (!result) return
    form.reset()
    props.onSaved()
    props.onClose()
  }

  return (
    <>
      <Dialog
        open={!operation.verification.isActive}
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
          <form
            id='gpt-account-form'
            onSubmit={form.handleSubmit(submit)}
            className='space-y-4'
          >
            {operation.error && (
              <p role='alert' className='text-destructive text-sm'>
                {t(operation.error)}
              </p>
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
                    'Separate model names with commas. Leave blank to use the default Codex models.'
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
          </form>
        </Form>
      </Dialog>
      <SecureVerificationDialog {...operation.verification.dialogProps} />
    </>
  )
}
