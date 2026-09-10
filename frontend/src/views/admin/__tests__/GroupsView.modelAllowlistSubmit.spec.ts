import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const {
  listGroups,
  updateGroup,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  showSuccess,
  showError
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  updateGroup: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

const authState = vi.hoisted(() => ({ isSimpleMode: false }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAll: vi.fn(),
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const savedModels = ['gpt-5.5', 'gpt-5.4']

const sourceGroup: AdminGroup = {
  id: 42,
  name: 'Primary',
  description: null,
  platform: 'openai',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  model_allowlist: { enabled: true, models: [...savedModels] },
  sort_order: 10
}

const emptyAllowlistGroup: AdminGroup = {
  ...sourceGroup,
  model_allowlist: { enabled: true, models: [] }
}

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>'
})

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false }
  },
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
})

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountView() {
  return mount(GroupsView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true
      }
    }
  })
}

function mockGroupList(group: AdminGroup) {
  listGroups.mockResolvedValue({
    items: [group],
    total: 1,
    page: 1,
    page_size: 20,
    pages: 1
  })
}

async function openEdit(wrapper: ReturnType<typeof mountView>) {
  const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
  expect(editButton).toBeTruthy()
  await editButton!.trigger('click')
  await flushPromises()
}

describe('GroupsView model allowlist submit', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    authState.isSimpleMode = false
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      updateGroup,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      showSuccess,
      showError
    ]) {
      fn.mockReset()
    }

    mockGroupList(sourceGroup)
    updateGroup.mockResolvedValue(sourceGroup)
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('submits saved allowlist models when candidates never resolve', async () => {
    getModelAllowlistCandidates.mockReturnValue(new Promise(() => {}))
    const wrapper = mountView()
    await flushPromises()
    await openEdit(wrapper)

    await wrapper.get('[data-tour="edit-group-form-name"]').setValue('Renamed')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
      name: 'Renamed',
      model_allowlist: { enabled: true, models: savedModels }
    }))
    expect(showError).not.toHaveBeenCalledWith('admin.groups.modelAllowlist.emptySelectionError')
    wrapper.unmount()
  })

  it('submits saved allowlist models when candidate loading fails', async () => {
    getModelAllowlistCandidates.mockRejectedValue(new Error('candidates failed'))
    const wrapper = mountView()
    await flushPromises()
    await openEdit(wrapper)

    await wrapper.get('[data-tour="edit-group-form-name"]').setValue('Renamed')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
      name: 'Renamed',
      model_allowlist: { enabled: true, models: savedModels }
    }))
    expect(showError).not.toHaveBeenCalledWith('admin.groups.modelAllowlist.emptySelectionError')
    wrapper.unmount()
  })

  it('blocks submit when allowlist is enabled with no saved models and no selections', async () => {
    mockGroupList(emptyAllowlistGroup)
    getModelAllowlistCandidates.mockResolvedValue([])
    const wrapper = mountView()
    await flushPromises()
    await openEdit(wrapper)

    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.groups.modelAllowlist.emptySelectionError')
    wrapper.unmount()
  })
})
