import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const {
  getHealthRuntime,
  getHealthConfig,
  updateHealthConfig,
  healthScan,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getHealthRuntime: vi.fn(),
  getHealthConfig: vi.fn(),
  updateHealthConfig: vi.fn(),
  healthScan: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => {
  const api = {
    proxies: {
      getHealthRuntime,
      getHealthConfig,
      updateHealthConfig,
      healthScan,
    },
  }
  return { default: api, adminAPI: api }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import ProxyHealthView from '../ProxyHealthView.vue'

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>',
})

const defaultSettings = {
  enabled: true,
  interval_sec: 60,
  timeout_ms: 10000,
  concurrency: 8,
  fail_threshold: 3,
  success_threshold: 2,
  probe_scope: 'group_members',
  auto_recover: true,
  skip_name_prefix: ['warp-'],
  leader_lock_ttl_sec: 50,
  batch_size: 100,
  probe_mode: 'connectivity',
}

const defaultRuntime = {
  config: defaultSettings,
  yaml_enabled: false,
  worker_running: true,
  worker_instance_id: 'n1',
  metrics: {
    ticks: 1,
    probed_total: 1,
    isolated_total: 0,
    recovered_total: 0,
    error_total: 0,
    skipped_total: 0,
    last_tick_unix: 1,
    last_scan_unix: 1,
  },
  last_tick_age_sec: 1,
  isolated_count: 0,
  recent_isolated: [],
  now_unix: 1,
}

function mountView() {
  return mount(ProxyHealthView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Icon: true,
        RouterLink: true,
      },
    },
  })
}

describe('ProxyHealthView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    for (const fn of [getHealthRuntime, getHealthConfig, updateHealthConfig, healthScan, showError, showSuccess]) {
      fn.mockReset()
    }
    getHealthRuntime.mockResolvedValue({ ...defaultRuntime })
    getHealthConfig.mockResolvedValue({ ...defaultSettings })
    updateHealthConfig.mockImplementation(async (payload) => payload)
    healthScan.mockResolvedValue({ probed: 1, isolated: 0, recovered: 0, skipped: 0, errors: 0 })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('auto-refresh only reloads runtime and keeps in-progress form edits', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getHealthConfig).toHaveBeenCalledTimes(1)
    expect(getHealthRuntime).toHaveBeenCalledTimes(1)

    const intervalInput = wrapper.findAll('input[type="number"]')[0]
    await intervalInput.setValue(90)
    expect((intervalInput.element as HTMLInputElement).value).toBe('90')

    getHealthRuntime.mockResolvedValue({ ...defaultRuntime, isolated_count: 9 })
    getHealthConfig.mockResolvedValue({ ...defaultSettings, interval_sec: 15 })

    await vi.advanceTimersByTimeAsync(30000)
    await flushPromises()

    expect(getHealthRuntime).toHaveBeenCalledTimes(2)
    expect(getHealthConfig).toHaveBeenCalledTimes(1)
    expect((wrapper.findAll('input[type="number"]')[0].element as HTMLInputElement).value).toBe('90')
    wrapper.unmount()
  })

  it('saves an empty skip-prefix as an empty list instead of defaulting to warp-', async () => {
    const wrapper = mountView()
    await flushPromises()

    const skipInput = wrapper.get('input[placeholder="admin.proxyHealth.skipNamePrefixHint"]')
    await skipInput.setValue('')
    await wrapper.findAll('button').find((btn) => btn.text() === 'admin.proxyHealth.save')?.trigger('click')
    await flushPromises()

    expect(updateHealthConfig).toHaveBeenCalledTimes(1)
    expect(updateHealthConfig.mock.calls[0]?.[0]?.skip_name_prefix).toEqual([])
    wrapper.unmount()
  })
})
