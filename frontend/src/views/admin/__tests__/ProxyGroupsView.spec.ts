import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { listGroups, getGroupById, getAllProxies, showError, showSuccess } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getGroupById: vi.fn(),
  getAllProxies: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => {
  const api = {
    proxyGroups: {
      list: listGroups,
      getById: getGroupById,
      update: vi.fn(),
      create: vi.fn(),
      delete: vi.fn(),
    },
    proxies: {
      getAll: getAllProxies,
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

import ProxyGroupsView from '../ProxyGroupsView.vue'

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>',
})

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
  },
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
})

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' },
  },
  template: '<div v-if="show" data-testid="group-form-dialog"><slot /><slot name="footer" /></div>',
})

const group = {
  id: 9,
  name: 'pool',
  description: '',
  strategy: 'round_robin',
  sticky_by_account: false,
  status: 'active',
  proxy_count: 2,
  created_at: '',
  updated_at: '',
}

function mountView() {
  return mount(ProxyGroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        Icon: true,
      },
    },
  })
}

describe('ProxyGroupsView edit detail failure', () => {
  beforeEach(() => {
    for (const fn of [listGroups, getGroupById, getAllProxies, showError, showSuccess]) fn.mockReset()
    listGroups.mockResolvedValue({ items: [group], total: 1 })
    getAllProxies.mockResolvedValue([])
  })

  it('shows an error and does not open the form when group detail fails', async () => {
    getGroupById.mockRejectedValue({ status: 500, code: 'GROUP_DETAIL_FAILED', message: 'detail failed' })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="common.edit"]').trigger('click')
    await flushPromises()

    expect(getGroupById).toHaveBeenCalledWith(9)
    expect(showError).toHaveBeenCalledWith('detail failed')
    expect(wrapper.find('[data-testid="group-form-dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('opens the form after a successful detail load', async () => {
    getGroupById.mockResolvedValue({ ...group, proxies: [{ id: 1 }, { id: 2 }] })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="common.edit"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="group-form-dialog"]').exists()).toBe(true)
    wrapper.unmount()
  })
})
