import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AccountModal from '../AccountTestModal.vue'
import AdminModal from '../../admin/account/AccountTestModal.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getAvailableModels: vi.fn().mockResolvedValue([{ id: 'synthetic-model', display_name: 'Synthetic' }]) } } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))

const fixtureDirectory = process.env.ACCOUNT_TIMING_FIXTURES_DIR
const legacy = 'data: {"type":"test_start","model":"synthetic-model"}\n\ndata: {"type":"content","text":"hello synthetic"}\n\ndata: {"type":"test_complete","success":true}\n\n'

function responseStream(text: string) {
  const bytes = new TextEncoder().encode(text)
  let offset = 0
  const reader = {
    read: vi.fn(async () => {
      if (offset >= bytes.length) return { done: true, value: undefined }
      const value = bytes.slice(offset, offset += 17)
      return { done: false, value }
    }),
    cancel: vi.fn().mockResolvedValue(undefined), releaseLock: vi.fn()
  }
  return { response: { ok: true, body: { getReader: () => reader } }, reader }
}

function mountModal(component: typeof AccountModal, locale = 'en') {
  return mount(component, {
    props: { show: false, account: { id: 42, name: 'Synthetic', platform: 'openai', type: 'apikey', credentials: {}, extra: {} } as any },
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { en, zh } })],
      stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, Select: true, TextArea: true, Icon: true }
    }
  })
}

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

for (const [name, component] of [['account', AccountModal], ['admin', AdminModal]] as const) {
  describe(`${name} real modal event consumer`, () => {
    it('keeps legacy success and shows unavailable timing, never a fabricated zero', async () => {
      const { response, reader } = responseStream(legacy)
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
      const wrapper = mountModal(component)
      await wrapper.setProps({ show: true }); await flushPromises()
      await (wrapper.vm as any).startTest(); await flushPromises()
      expect(wrapper.get('[data-testid="account-test-timing"]').text()).toContain('—')
      expect(wrapper.text()).toContain('Timing was not supplied')
      expect(reader.releaseLock).toHaveBeenCalledOnce()
      expect(wrapper.find('details[open]').exists()).toBe(false)
      wrapper.unmount()
    })

    it('rejects an incomplete stream and releases the reader', async () => {
      const { response, reader } = responseStream('data: {"type":"test_start"}\n\n: heartbeat\n\n')
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
      const wrapper = mountModal(component)
      await wrapper.setProps({ show: true }); await flushPromises()
      await (wrapper.vm as any).startTest(); await flushPromises()
      expect(wrapper.get('[data-testid="account-test-timing"]').text()).toContain('Stream ended before completion')
      expect(reader.releaseLock).toHaveBeenCalledOnce()
      wrapper.unmount()
    })

    it('cancels on unmount and prevents an old stream from updating a new test', async () => {
      let resolveRead: (value: unknown) => void = () => {}
      const reader = { read: vi.fn(() => new Promise(resolve => { resolveRead = resolve })), cancel: vi.fn(async () => { resolveRead({ done: true }) }), releaseLock: vi.fn() }
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, body: { getReader: () => reader } }))
      const wrapper = mountModal(component)
      await wrapper.setProps({ show: true }); await flushPromises()
      const request = (wrapper.vm as any).startTest(); await flushPromises()
      await wrapper.setProps({ show: false }); await request
      await flushPromises()
      expect(wrapper.get('[data-testid="account-test-timing"]').text()).toContain('Canceled in this browser')
      expect(reader.cancel).toHaveBeenCalled()
      expect(reader.releaseLock).toHaveBeenCalledOnce()
      wrapper.unmount()
    })

    describe.skipIf(!fixtureDirectory)('captured real handler → service → synthetic HTTP/SSE → UI', () => {
      for (const locale of ['en', 'zh']) {
        for (const scenario of ['apikey-responses', 'oauth-responses', 'apikey-chat', 'upstream-error', 'incomplete', 'non-stream']) {
          it(`${locale}: ${scenario}`, async () => {
            const text = readFileSync(join(fixtureDirectory!, `${scenario}.sse`), 'utf8')
            const { response, reader } = responseStream(text)
            vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
            const wrapper = mountModal(component, locale)
            await wrapper.setProps({ show: true }); await flushPromises()
            await (wrapper.vm as any).startTest(); await flushPromises()
            const panel = wrapper.get('[data-testid="account-test-timing"]')
            expect(panel.text()).toContain(locale === 'en' ? 'Total' : '总耗时')
            expect(panel.text()).not.toContain('accountTestTiming.')
            expect(panel.text()).toMatch(/UTC|GMT/)
            expect(panel.find('details').attributes('open')).toBeUndefined()
            await panel.get('summary').trigger('click')
            if (scenario.endsWith('responses') || scenario === 'apikey-chat') {
              expect(wrapper.text()).toContain('hello synthetic')
              expect(panel.get('[data-metric="first_content"]').text()).toMatch(/\d/)
              expect(panel.get('[data-metric="stream_output"]').text()).toMatch(/\d/)
            }
            if (scenario === 'non-stream') expect(panel.get('[data-metric="first_content"]').text()).toContain('—')
            expect(reader.releaseLock).toHaveBeenCalledOnce()
            wrapper.unmount()
          })
        }
      }
    })
  })
}
