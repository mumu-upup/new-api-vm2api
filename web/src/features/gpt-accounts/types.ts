export type GptAccountStatus =
  | 'pending'
  | 'logging_in'
  | 'ready'
  | 'needs_attention'
  | 'error'

export interface GptAccount {
  id: number
  protocol?: '' | 'codex' | 'excel_bps'
  name: string
  email: string
  channel_id?: number
  channel_name?: string
  gateway_id?: number
  gateway_name?: string
  gateway_provider?: 'cpa' | 'sub2api' | 'vm2api'
  gateway_channel_id?: number
  gateway_channel_name?: string
  gateway_sync_status?: '' | 'pending' | 'synced' | 'error'
  gateway_error_code?: string
  gateway_synced_at?: number
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
  totp_version: number
  totp_updated_at: number
  totp_status: '' | 'saved' | 'operator_confirmed' | 'automated'
  totp_provider?: '' | 'local' | '2fa_cn'
  can_manage: boolean
  group: string
  models: string
}

export interface GptAccountInput {
  protocol?: 'codex' | 'excel_bps'
  name: string
  email: string
  password: string
  totp_secret: string
  totp_provider: 'local' | '2fa_cn'
  auto_relogin: boolean
  group: string
  models: string
  channel_id?: number
  gateway_id?: number
}

export interface GptAccountGateway {
  id: number
  name: string
  provider: 'cpa' | 'sub2api' | 'vm2api'
  management_version?: '' | 'v8' | 'legacy'
  management_url?: string
  relay_url?: string
  allow_local_http?: boolean
  remote_group_id?: number
  group?: string
  models?: string
  channel_id?: number
  channel_name?: string
  has_management_key?: boolean
  has_relay_key?: boolean
}

export interface GptAccountGatewayInput {
  name: string
  provider: 'cpa' | 'sub2api' | 'vm2api'
  management_version: '' | 'v8' | 'legacy'
  management_url: string
  relay_url: string
  allow_local_http: boolean
  remote_group_id: number
  group: string
  models: string
  management_key: string
  relay_key: string
}

export interface GptAccountConfig {
  default_group: string
  default_models: string
  encryption_available: boolean
  automation_available: boolean
  default_gateway_id?: number
}

export interface GptAccountVm2ApiStatus {
  account_id: number
  vm_id: string
  gateway_id: number
  vm: Record<string, unknown>
  quota?: Record<string, unknown>
}

export interface GptAccountVm2ApiLogs {
  items?: Array<Record<string, unknown>>
  total?: number
  [key: string]: unknown
}

export interface GptAccountAuthorization {
  authorization_url: string
  expires_at: number
}

export interface GptAccountTotpReveal {
  totp_secret: string
  expires_at: number
}

export interface GptAccountTotpChangeResult {
  stage: 'primary' | 'fallback'
  fallback_used: boolean
}

export interface GptAccountVm2ApiInstance {
  account_id: number
  isolation: 'shared' | 'dedicated'
  instance_id?: string
  gateway_id?: number
  source_gateway_id?: number
  status: string
  runtime_status?: string
  error_code?: string
  container_name?: string
  data_directory?: string
  image?: string
  port?: number
  updated_at?: number
  vm_id?: string
}
export type Vm2ApiInstanceAction =
  | 'start'
  | 'stop'
  | 'restart'
  | 'rebuild'
  | 'provision'
