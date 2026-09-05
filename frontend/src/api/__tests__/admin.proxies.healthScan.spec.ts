import { beforeEach, describe, expect, it, vi } from 'vitest'

const apiClientMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: apiClientMock,
}))

import { healthScan } from '@/api/admin/proxies'

describe('admin proxies healthScan', () => {
  beforeEach(() => {
    apiClientMock.post.mockReset().mockResolvedValue({
      data: { probed: 1, isolated: 0, recovered: 0, skipped: 0, errors: 0 },
    })
  })

  it('uses a 130s timeout so the request outlives the backend 2min scan', async () => {
    await healthScan()
    expect(apiClientMock.post).toHaveBeenCalledTimes(1)
    expect(apiClientMock.post).toHaveBeenCalledWith(
      '/admin/proxies/health-scan',
      undefined,
      { timeout: 130_000 },
    )
  })
})
