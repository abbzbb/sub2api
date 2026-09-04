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

const grokHasPaidTierEvidence = (account: RateLimitAccount, snap: Record<string, unknown>): boolean => {
  const extra = account.extra as Record<string, unknown> | undefined
  const credentials = account.credentials as Record<string, unknown> | undefined
  for (const source of [credentials, extra]) {
    if (!source) continue
    for (const key of ['subscription_tier', 'plan', 'plan_type', 'entitlement_status']) {
      if (grokPaidTierEvidence(source[key])) return true
    }
  }
  return grokPaidTierEvidence(snap.subscription_tier) || grokPaidTierEvidence(snap.entitlement_status)
}

const grokQuotaSnapshotIsStale = (snap: Record<string, unknown>, now: number): boolean => {
  const raw = String(snap.updated_at ?? snap.last_probe_at ?? '').trim()
  if (!raw) return false
  const updated = Date.parse(raw)
  return Number.isFinite(updated) && now - updated > grokQuotaSnapshotMaxAgeMs
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
    tokens?: { remaining?: number | null; reset_at?: string | null }
    requests?: { remaining?: number | null; reset_at?: string | null }
  }
  const code = String(snap.provider_error_code ?? '').trim().toLowerCase()
  if (grokFreeUsageExhaustedCodes.has(code)) return true
  const remainingZero = (window?: { remaining?: number | null; reset_at?: string | null }) =>
    window != null && typeof window.remaining === 'number' && window.remaining <= 0
  if (!remainingZero(snap.tokens) && !remainingZero(snap.requests)) return false
  // 与后端 grokQuotaSnapshotBlocksSchedulingFromSnapshot 对齐：
  // 已知重置时间 → 仅在重置时间尚未到达时视为受限；
  // remaining=0 无 reset：付费账号不闩；过期快照不闩；免费/未知保持受限。
  let sawResetAt = false
  for (const window of [snap.tokens, snap.requests]) {
    const resetRaw = window?.reset_at
    if (!resetRaw) continue
    const resetAt = Date.parse(resetRaw)
    if (!Number.isFinite(resetAt)) continue
    sawResetAt = true
    if (resetAt > now) return true
  }
  if (sawResetAt) return false
  if (grokQuotaSnapshotIsStale(snap, now)) return false
  return !grokHasPaidTierEvidence(account, snap)
}
