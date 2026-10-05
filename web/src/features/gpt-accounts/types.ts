export type GptAccountStatus =
  | 'pending'
  | 'logging_in'
  | 'ready'
  | 'needs_attention'
  | 'error'

export interface GptAccount {
  id: number
  name: string
  email: string
  channel_id?: number
  channel_name?: string
  status: GptAccountStatus
  last_error_code: string
  last_login_at: number
  last_attempt_at: number
  next_retry_at: number
  created_at: number
  updated_at: number
  auto_relogin: boolean
  has_password: boolean
  has_totp: boolean
  can_manage: boolean
  group: string
  models: string
}

export interface GptAccountInput {
  name: string
  email: string
  password: string
  totp_secret: string
  auto_relogin: boolean
  group: string
  models: string
  channel_id?: number
}

export interface GptAccountConfig {
  encryption_available: boolean
  automation_available: boolean
}

export interface GptAccountAuthorization {
  authorization_url: string
  expires_at: number
}
