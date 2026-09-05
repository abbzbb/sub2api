import { describe, expect, it } from 'vitest'
import type { Account } from '@/types'
import { isAccountRateLimited, isGrokFreeRecoveryPending } from '../accountStatus'

const makeAccount = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'account',
  platform: 'grok',
  type: 'oauth',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: true,
  created_at: '2026-07-14T00:00:00Z',
  updated_at: '2026-07-14T00:00:00Z',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

describe('accountStatus', () => {
  it('keeps a pending Grok Free account rate limited after its reset timestamp expires', () => {
    const account = makeAccount({
      grok_free_recovery_pending: true,
      rate_limit_reset_at: '2026-07-14T00:01:00Z'
    })

    expect(isGrokFreeRecoveryPending(account)).toBe(true)
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:02:00Z'))).toBe(true)
  })

  it('keeps timestamp behavior for ordinary accounts', () => {
    const account = makeAccount({
      grok_free_recovery_pending: false,
      rate_limit_reset_at: '2026-07-14T00:03:00Z'
    })

    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:02:00Z'))).toBe(true)
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:04:00Z'))).toBe(false)
  })

  it('treats a persisted Grok remaining=0 snapshot as rate limited', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          provider_error_code: 'subscription:free-usage-exhausted',
          tokens: { remaining: 0 }
        }
      }
    })

    expect(isAccountRateLimited(account)).toBe(true)
  })

  it('keeps a free Grok remaining=0 snapshot blocked after a past reset_at', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          tokens: { remaining: 0, reset_at: '2026-07-14T00:01:00Z' },
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })

    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:00:30Z'))).toBe(true)
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:02:00Z'))).toBe(true)
  })

  it('honors a future reset_unix and retry_after_seconds on a Grok snapshot', () => {
    const unixAccount = makeAccount({
      extra: {
        grok_usage_snapshot: {
          tokens: { remaining: 0, reset_unix: Math.floor(Date.parse('2026-07-14T00:03:00Z') / 1000) }
        }
      }
    })
    expect(isAccountRateLimited(unixAccount, Date.parse('2026-07-14T00:02:00Z'))).toBe(true)
    expect(isAccountRateLimited(unixAccount, Date.parse('2026-07-14T00:04:00Z'))).toBe(true)

    const retryAccount = makeAccount({
      extra: {
        grok_usage_snapshot: {
          tokens: { remaining: 0 },
          retry_after_seconds: 120,
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })
    expect(isAccountRateLimited(retryAccount, Date.parse('2026-07-14T00:01:00Z'))).toBe(true)
    expect(isAccountRateLimited(retryAccount, Date.parse('2026-07-14T00:03:00Z'))).toBe(true)
  })

  it('treats grok_billing_snapshot paid evidence as not rate limited for remaining=0', () => {
    const account = makeAccount({
      extra: {
        grok_billing_snapshot: { plan: 'supergrok', monthly_limit_cents: 2000 },
        grok_usage_snapshot: {
          tokens: { remaining: 0 },
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:01:00Z'))).toBe(false)
  })

  it('releases a stale free-usage-exhausted snapshot older than 6 hours', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          provider_error_code: 'subscription:free-usage-exhausted',
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T07:00:00Z'))).toBe(false)
  })

  it('keeps a Grok remaining=0 snapshot without any reset_at rate limited', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          requests: { remaining: 0 }
        }
      }
    })

    expect(isAccountRateLimited(account)).toBe(true)
  })

  it('ignores extra.plan paid markers that the backend does not read', () => {
    const account = makeAccount({
      extra: {
        plan: 'supergrok',
        grok_usage_snapshot: {
          tokens: { remaining: 0 },
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T00:01:00Z'))).toBe(true)
  })

  it('falls back to last_probe_at when updated_at is an empty string', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          requests: { remaining: 0 },
          updated_at: '',
          last_probe_at: '2026-07-14T00:00:00Z'
        }
      }
    })
    expect(isAccountRateLimited(account, Date.parse('2026-07-14T07:00:00Z'))).toBe(false)
  })

  it('does not treat a paid Grok remaining=0 snapshot without reset as rate limited', () => {
    const account = makeAccount({
      credentials: { subscription_tier: 'supergrok' },
      extra: {
        grok_usage_snapshot: {
          subscription_tier: 'supergrok',
          tokens: { remaining: 0 }
        }
      }
    })

    expect(isAccountRateLimited(account)).toBe(false)
  })

  it('releases a stale Grok remaining=0 snapshot older than 6 hours', () => {
    const account = makeAccount({
      extra: {
        grok_usage_snapshot: {
          requests: { remaining: 0 },
          updated_at: '2026-07-14T00:00:00Z'
        }
      }
    })

    expect(isAccountRateLimited(account, Date.parse('2026-07-14T07:00:00Z'))).toBe(false)
  })

  it('ignores a misplaced Grok recovery marker on another platform', () => {
    const account = makeAccount({
      platform: 'openai',
      grok_free_recovery_pending: true
    })

    expect(isGrokFreeRecoveryPending(account)).toBe(false)
    expect(isAccountRateLimited(account)).toBe(false)
  })
})
