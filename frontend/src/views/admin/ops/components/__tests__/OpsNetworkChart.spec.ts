import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import OpsNetworkChart from '../OpsNetworkChart.vue'
import en from '@/i18n/locales/en/latencyObservability'
const mocks = vi.hoisted(() => ({ read: vi.fn() }))
vi.mock('@/api/admin/opsNetwork', () => ({ getNetworkSeries: mocks.read }))
it('Issue603 renders real minute samples, measured zero and unsupported qdisc without a false zero curve', async () => {
  mocks.read.mockResolvedValue({ points: [
    { at: '2026-10-30T00:00:00Z', instance: 'fixture', egress_mbps: null, tcp_retrans_percent: null, qdisc_drops: null, egress_status: 'initial_sample', tcp_status: 'initial_sample', qdisc_status: 'unsupported_unprivileged_collector' },
    { at: '2026-10-30T00:01:00Z', instance: 'fixture', egress_mbps: 0, tcp_retrans_percent: 1, qdisc_drops: null, egress_status: 'measured', tcp_status: 'measured', qdisc_status: 'unsupported_unprivileged_collector' }
  ], body_points: [{ at: '2026-10-30T00:01:00Z', bytes: 1048577, samples: 2 }] })
  const w = mount(OpsNetworkChart, { global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { latencyObservability: en } } })] } })
  await flushPromises()
  expect(w.get('[data-testid="network-egress"]').text()).toContain('0 · measured')
  expect(w.get('[data-testid="network-qdisc"]').text()).toContain('Not collected · unsupported_unprivileged_collector')
  expect(w.get('[data-testid="network-qdisc"] path').attributes('d')).toBe('')
  expect(w.get('[data-testid="network-bodyBytes"]').text()).toContain('1048577')
  w.unmount()
})
