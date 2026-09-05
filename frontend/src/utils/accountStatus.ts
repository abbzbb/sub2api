import type { Account } from '@/types'

type RateLimitAccount = Pick<
  Account,
  'platform' | 'type' | 'grok_free_recovery_pending' | 'rate_limit_reset_at' | 'extra'
> & {
  credentials?: Record<string, unknown> | null
}

export const isGrokFreeRecoveryPending = (
  account: RateLimitAccount | null | undefined
): boolean => {
  return account?.platform === 'grok' &&
    account.type === 'oauth' &&
    account.grok_free_recovery_pending === true
}

export const isAccountRateLimited = (
  account: RateLimitAccount | null | undefined,
  now = Date.now()
): boolean => {
  if (!account) return false
  if (isGrokFreeRecoveryPending(account)) return true
  if (grokExtraQuotaSnapshotBlocksScheduling(account, now)) return true
  if (!account.rate_limit_reset_at) return false

  const resetAt = Date.parse(account.rate_limit_reset_at)
  return Number.isFinite(resetAt) && resetAt > now
}

const grokFreeUsageExhaustedCodes = new Set([
  'subscription:free-usage-exhausted',
  'subscription-free-usage-exhausted'
])

const grokQuotaSnapshotMaxAgeMs = 6 * 60 * 60 * 1000

const grokPaidTierMarkers = [
  'supergrok',
  'super_grok',
  'heavy',
  'premium',
  'enterprise',
  'ultra',
  'paid',
  'pro'
]

const grokPaidTierEvidence = (raw: unknown): boolean => {
  const value = String(raw ?? '').trim().toLowerCase()
  if (!value || value === 'free' || value === 'basic' || value === 'unknown') return false
  return grokPaidTierMarkers.some((marker) => value.includes(marker))
}

const grokHasPaidBillingSnapshot = (extra: Record<string, unknown> | undefined): boolean => {
  const billing = extra?.grok_billing_snapshot
  if (!billing || typeof billing !== 'object') return false
  const snap = billing as Record<string, unknown>
  if (snap.usage_percent != null || snap.used_percent != null) return true
  if (typeof snap.monthly_limit_cents === 'number' && snap.monthly_limit_cents > 0) return true
  return grokPaidTierEvidence(snap.plan)
}

const grokHasPaidTierEvidence = (account: RateLimitAccount, snap: Record<string, unknown>): boolean => {
  const extra = account.extra as Record<string, unknown> | undefined
  const credentials = account.credentials as Record<string, unknown> | undefined
  for (const source of [credentials, extra]) {
    if (!source) continue
    for (const key of ['subscription_tier', 'plan', 'plan_type', 'entitlement_status']) {
      if (grokPaidTierEvidence(source[key])) return true
    }
  }
  if (grokPaidTierEvidence(snap.subscription_tier) || grokPaidTierEvidence(snap.entitlement_status)) {
    return true
  }
  return grokHasPaidBillingSnapshot(extra)
}

const grokQuotaSnapshotIsStale = (snap: Record<string, unknown>, now: number): boolean => {
  const raw = String(snap.updated_at ?? snap.last_probe_at ?? '').trim()
  if (!raw) return false
  const updated = Date.parse(raw)
  return Number.isFinite(updated) && now - updated > grokQuotaSnapshotMaxAgeMs
}

type GrokQuotaWindow = {
  remaining?: number | null
  reset_at?: string | null
  reset_unix?: number | null
}

const grokSnapshotAbsoluteResetAt = (
  snap: {
    retry_after_seconds?: number | null
    updated_at?: string
    tokens?: GrokQuotaWindow
    requests?: GrokQuotaWindow
  },
  now: number
): number => {
  let resetAt = 0
  const retry = snap.retry_after_seconds
  if (typeof retry === 'number' && retry > 0) {
    let observedAt = now
    const updated = Date.parse(String(snap.updated_at ?? '').trim())
    if (Number.isFinite(updated)) observedAt = updated
    const candidate = observedAt + retry * 1000
    if (candidate > now) resetAt = candidate
  }
  for (const window of [snap.tokens, snap.requests]) {
    if (!window) continue
    let candidate = 0
    if (typeof window.reset_unix === 'number' && window.reset_unix > 0) {
      candidate = window.reset_unix * 1000
    } else if (window.reset_at) {
      const parsed = Date.parse(String(window.reset_at).trim())
      if (Number.isFinite(parsed)) candidate = parsed
    }
    if (candidate > now && candidate > resetAt) resetAt = candidate
  }
  return resetAt
}

const grokExtraQuotaSnapshotBlocksScheduling = (
  account: RateLimitAccount,
  now: number
): boolean => {
  if (account.platform !== 'grok' || account.type !== 'oauth') return false
  const extra = account.extra as Record<string, unknown> | undefined
  const snapshot = extra?.grok_usage_snapshot
  if (!snapshot || typeof snapshot !== 'object') return false
  const snap = snapshot as {
    provider_error_code?: string
    subscription_tier?: string
    entitlement_status?: string
    updated_at?: string
    last_probe_at?: string
    retry_after_seconds?: number | null
    tokens?: GrokQuotaWindow
    requests?: GrokQuotaWindow
  }
  const code = String(snap.provider_error_code ?? '').trim().toLowerCase()
  const stale = grokQuotaSnapshotIsStale(snap, now)
  if (grokFreeUsageExhaustedCodes.has(code) && !stale) return true
  const remainingZero = (window?: GrokQuotaWindow) =>
    window != null && typeof window.remaining === 'number' && window.remaining <= 0
  if (!remainingZero(snap.tokens) && !remainingZero(snap.requests)) return false
  const resetAt = grokSnapshotAbsoluteResetAt(snap, now)
  if (resetAt > 0) return resetAt > now
  if (stale) return false
  return !grokHasPaidTierEvidence(account, snap)
}
