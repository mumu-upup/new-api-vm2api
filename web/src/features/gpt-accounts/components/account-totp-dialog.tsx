import { zodResolver } from '@hookform/resolvers/zod'
import { ExternalLink, Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'

import { changeGptAccountTotp, revealGptAccountTotp, saveGptAccountTotp } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import { accountTotpSchema, type AccountTotpValues } from '../lib/account-form'
import type { GptAccount, GptAccountTotpReveal } from '../types'

interface AccountTotpDialogProps {
  account: GptAccount
  mode: 'save' | 'reveal' | 'change'
  onClose: () => void
  onSaved: () => void
}

export function AccountTotpDialog(props: AccountTotpDialogProps) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const [revealed, setRevealed] = useState<GptAccountTotpReveal | null>(null)
  const form = useForm<AccountTotpValues>({
    resolver: zodResolver(accountTotpSchema),
    defaultValues: { totp_secret: '', confirmed_bound: false },
  })
  const onClose = props.onClose
  useEffect(() => {
    const hide = () => {
      if (document.visibilityState === 'hidden') onClose()
    }
    document.addEventListener('visibilitychange', hide)
    window.addEventListener('pagehide', onClose)
    return () => {
      document.removeEventListener('visibilitychange', hide)
      window.removeEventListener('pagehide', onClose)
    }
  }, [onClose])
  useEffect(() => {
    if (!revealed) return
    const expiresIn = Math.min(30_000, revealed.expires_at * 1000 - Date.now())
    const timeout = setTimeout(onClose, Math.max(0, expiresIn))
    const checkExpiry = () => {
      if (Date.now() >= revealed.expires_at * 1000) onClose()
    }
    window.addEventListener('focus', checkExpiry)
    return () => {
      clearTimeout(timeout)
      window.removeEventListener('focus', checkExpiry)
    }
  }, [revealed, onClose])

  const save = async (values: AccountTotpValues) => {
    form.reset()
    if (!props.account.can_manage) return
    const saved = await operation.execute((signal) =>
      saveGptAccountTotp(props.account.id, values, signal)
    )
    if (!saved) return
    props.onSaved()
    props.onClose()
  }
  const reveal = async () => {
    if (!props.account.can_manage || !props.account.has_totp) return
    await operation.execute(async (signal) => {
      const result = await revealGptAccountTotp(props.account.id, signal)
      signal.throwIfAborted()
      if (
        document.visibilityState === 'hidden' ||
        result.expires_at * 1000 <= Date.now()
      ) {
        return
      }
      // Only the mounted dialog owns the secret; mutation results stay empty.
      setRevealed({
        ...result,
        expires_at: Math.min(result.expires_at, Date.now() / 1000 + 30),
      })
    })
  }
  const change = async () => {
    if (!props.account.can_manage || !props.account.has_totp) return
    const changed = await operation.execute((signal) =>
      changeGptAccountTotp(props.account.id, signal)
    )
    if (!changed) return
    props.onSaved()
    props.onClose()
  }
  let title = t('Current TOTP secret')
  if (props.mode === 'save') title = t('Save replacement secret')
  if (props.mode === 'change') title = t('Change 2FA automatically')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={title}
      description={`${props.account.name} · ${props.account.email}`}
      footer={
        <>
          <Button variant='outline' onClick={props.onClose}>
            {revealed ? t('Done') : t('Cancel')}
          </Button>
          {props.mode === 'save' && (
            <Button
              type='submit'
              form='gpt-account-totp-form'
              disabled={operation.pending}
            >
              {operation.pending && <Loader2 className='animate-spin' />}
              {t('Save replacement secret')}
            </Button>
          )}
          {props.mode === 'reveal' && !revealed && (
            <Button
              onClick={() => {
                void reveal()
              }}
              disabled={operation.pending}
            >
              {operation.pending && <Loader2 className='animate-spin' />}
              {t('Reveal secret')}
            </Button>
          )}
          {props.mode === 'change' && (
            <Button
              onClick={() => void change()}
              disabled={operation.pending}
            >
              {operation.pending && <Loader2 className='animate-spin' />}
              {t('Start automatic 2FA change')}
            </Button>
          )}
        </>
      }
    >
      <div className='space-y-4'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Complete authenticator binding on the official OpenAI website before saving. This gateway does not verify upstream binding.'
          )}
        </p>
        {operation.error && (
          <p role='alert' className='text-destructive text-sm'>
            {operation.error}
          </p>
        )}
        {props.mode === 'change' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'The gateway will verify the replacement before saving it. If the official flow fails, the configured Moyanovo fallback may be used once.'
            )}
          </p>
        )}
        {props.mode === 'save' && (
          <>
            <p className='text-muted-foreground text-sm'>
              {t(
                'On the official website, replace the authenticator yourself. After binding the new secret, return here to save it.'
              )}
            </p>
            <Button
              nativeButton={false}
              role='link'
              variant='outline'
              render={
                <a
                  href='https://chatgpt.com/#settings/Security'
                  target='_blank'
                  rel='noopener noreferrer'
                  referrerPolicy='no-referrer'
                />
              }
            >
              <ExternalLink />
              {t('Open ChatGPT security settings')}
            </Button>
            <Form {...form}>
              <form
                id='gpt-account-totp-form'
                onSubmit={form.handleSubmit(save)}
                className='space-y-4'
              >
                <FormField
                  control={form.control}
                  name='totp_secret'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Replacement TOTP secret')}</FormLabel>
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
                <FormField
                  control={form.control}
                  name='confirmed_bound'
                  render={({ field }) => (
                    <FormItem>
                      <div className='flex items-start gap-2'>
                        <FormControl>
                          <Checkbox
                            checked={field.value}
                            onCheckedChange={field.onChange}
                            disabled={operation.pending}
                          />
                        </FormControl>
                        <FormLabel>
                          {t(
                            'I completed binding this secret on the official OpenAI website.'
                          )}
                        </FormLabel>
                      </div>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </form>
            </Form>
          </>
        )}
        {props.mode === 'reveal' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'The secret is hidden after 30 seconds or when you leave this dialog or browser tab. Copied secrets remain on your clipboard.'
            )}
          </p>
        )}
        {revealed && (
          <div className='flex items-center gap-2'>
            <code className='min-w-0 flex-1 break-all select-all'>
              {revealed.totp_secret}
            </code>
            <CopyButton value={revealed.totp_secret} />
          </div>
        )}
      </div>
    </Dialog>
  )
}
