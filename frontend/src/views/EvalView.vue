<template>
  <div class="eval-page">
    <div class="page-heading mb-4">
      <div class="text-h5 font-weight-bold">评测</div>
      <div class="d-flex flex-wrap ga-2">
        <v-btn variant="text" prepend-icon="mdi-refresh" :loading="loading" @click="reloadAll">刷新</v-btn>
        <v-btn variant="text" prepend-icon="mdi-history" @click="historyDrawer = true">历史</v-btn>
        <v-btn variant="text" prepend-icon="mdi-format-list-bulleted" @click="probeDrawer = true">题目</v-btn>
      </div>
    </div>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-4" closable @click:close="error = ''">{{ error }}</v-alert>

    <v-card class="mb-4 pa-4" elevation="0" border>
      <div v-if="selectedSuiteDescription" class="text-caption text-medium-emphasis mb-3">
        {{ selectedSuiteDescription }}
      </div>
      <div class="d-flex flex-wrap align-center ga-3">
        <v-select
          v-model="selectedSuiteId"
          :items="suiteItems"
          item-title="title"
          item-value="value"
          label="套件"
          density="compact"
          variant="outlined"
          hide-details
          style="min-width: 200px"
        />
        <span class="text-body-2 text-medium-emphasis">已选 {{ selectedChannelIds.length }}</span>
        <v-spacer />

        <v-menu v-model="optionsMenu" :close-on-content-click="false" location="bottom end">
          <template #activator="{ props: activator }">
            <v-btn v-bind="activator" variant="text" size="small" prepend-icon="mdi-tune">选项</v-btn>
          </template>
          <v-card min-width="280">
            <v-card-text>
              <v-select
                v-model="thinking"
                :items="THINKING_ITEMS"
                item-title="title"
                item-value="value"
                label="思考"
                density="compact"
                variant="outlined"
                hide-details
                class="mb-3"
              />
              <v-text-field
                v-model.trim="modelOverride"
                label="统一模型"
                placeholder="空则用各渠道默认"
                density="compact"
                variant="outlined"
                hide-details
              />
            </v-card-text>
          </v-card>
        </v-menu>

        <v-menu v-model="watchMenu" :close-on-content-click="false" location="bottom end">
          <template #activator="{ props: activator }">
            <v-btn v-bind="activator" variant="text" size="small" prepend-icon="mdi-clock-outline">
              {{ watchButtonLabel }}
            </v-btn>
          </template>
          <v-card min-width="320">
            <v-card-text>
              <v-switch v-model="watchForm.enabled" label="定时复查" color="primary" density="compact" hide-details class="mb-2" />
              <v-select
                v-model="watchForm.suiteId"
                :items="cheapSuiteItems"
                item-title="title"
                item-value="value"
                label="套件"
                density="compact"
                variant="outlined"
                hide-details
                class="mb-3"
              />
              <v-select
                v-model="watchForm.interval"
                :items="INTERVAL_ITEMS"
                item-title="title"
                item-value="value"
                label="间隔"
                density="compact"
                variant="outlined"
                hide-details
              />
            </v-card-text>
            <v-card-actions>
              <v-spacer />
              <v-btn variant="text" size="small" @click="watchMenu = false">取消</v-btn>
              <v-btn
                color="primary"
                size="small"
                :loading="savingWatch"
                :disabled="watchForm.enabled && selectedChannelIds.length === 0"
                @click="saveWatch"
              >
                保存
              </v-btn>
            </v-card-actions>
          </v-card>
        </v-menu>

        <v-btn v-if="busy" color="error" variant="tonal" :loading="cancelling" @click="cancelRun">取消</v-btn>
        <v-btn v-else color="primary" :loading="starting" :disabled="!canStart" @click="startRun">开始</v-btn>
      </div>

      <div class="mt-4">
        <EvalChannelPicker
          v-model="selectedChannelIds"
          v-model:protocols="enabledProtocols"
          v-model:channel-models="channelModels"
          :channels-by-kind="channelsByKind"
          :pools-by-kind="poolsByKind"
        />
      </div>
    </v-card>

    <v-card v-if="currentRun" class="pa-4" elevation="0" border>
      <div class="d-flex flex-wrap align-center ga-2 mb-3">
        <div class="font-weight-medium">结果</div>
        <v-chip size="x-small" variant="tonal" :color="evalRunStatusColor(currentRun.status)">
          {{ evalRunStatusLabel(currentRun.status) }}
        </v-chip>
        <span class="text-caption text-medium-emphasis">
          {{ currentRun.suiteName || '' }} · {{ evalFormatTime(currentRun.startedAt) }}
        </span>
      </div>

      <v-alert v-if="currentRun?.error" type="warning" variant="tonal" density="compact" class="mb-3">
        {{ currentRun.error }}
      </v-alert>

      <EvalMatrix :run="currentRun" :probes="matrixProbes" :channels-by-kind="channelsByKind" @cell="openCell" />
    </v-card>

    <v-navigation-drawer v-model="historyDrawer" location="end" temporary width="480">
      <div class="pa-4">
        <div class="d-flex align-center ga-2 mb-4">
          <div class="text-h6">评测历史</div>
          <v-spacer />
          <v-btn icon="mdi-close" size="small" variant="text" @click="historyDrawer = false" />
        </div>

        <div v-if="!runHistory.length" class="text-body-2 text-medium-emphasis">还没有评测记录。</div>

        <v-list v-else density="compact" bg-color="transparent">
          <v-list-item
            v-for="item in runHistory"
            :key="item.id"
            class="px-2 mb-1 run-history-item"
            :active="currentRun?.id === item.id"
            :ripple="false"
            @click="selectRun(item.id)"
          >
            <v-list-item-title class="text-body-2 d-flex align-center ga-2">
              {{ item.suiteName || '未知套件' }}
              <v-chip size="x-small" variant="tonal" :color="evalRunStatusColor(item.status)">
                {{ evalRunStatusLabel(item.status) }}
              </v-chip>
            </v-list-item-title>
            <v-list-item-subtitle class="text-caption">
              {{ item.channelIds.length }} 个渠道
              <template v-if="item.trigger === 'watch'"> · 定时</template>
              ·
              {{ evalFormatTime(item.startedAt || item.createdAt) }}
            </v-list-item-subtitle>
          </v-list-item>
        </v-list>
      </div>
    </v-navigation-drawer>

    <EvalResultDrawer v-model="cellDrawer" :selection="cellSelection" :channel-name-of="channelNameOf" />
    <EvalProbeManager v-model="probeDrawer" :probes="probes" @changed="reloadProbes" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import EvalChannelPicker from '@/components/EvalChannelPicker.vue'
import EvalMatrix from '@/components/EvalMatrix.vue'
import EvalProbeManager from '@/components/EvalProbeManager.vue'
import EvalResultDrawer from '@/components/EvalResultDrawer.vue'
import {
  api,
  channelApiByType,
  type ApiTab,
  type Channel,
  type ChannelPool,
  type EvalProbe,
  type EvalRun,
  type EvalSuite,
  type EvalWatchConfig
} from '@/services/api'
import { usePreferencesStore } from '@/stores/preferences'
import {
  EVAL_PROTOCOL_KINDS,
  evalFormatTime,
  evalIntervalLabel,
  evalRunStatusColor,
  evalRunStatusLabel,
  type EvalCellSelection
} from '@/utils/eval'

const THINKING_ITEMS = [
  { title: '跟随题目', value: 'inherit' },
  { title: '关闭思考', value: 'off' },
  { title: '开启思考', value: 'enabled' }
]
const INTERVAL_ITEMS = [
  { title: '30 分钟', value: '30m' },
  { title: '2 小时', value: '2h' },
  { title: '1 天', value: '1d' }
]

const emptyByKind = <T,>(): Record<ApiTab, T[]> => ({
  messages: [],
  responses: [],
  gemini: [],
  chat: [],
  images: []
})

const route = useRoute()
const preferences = usePreferencesStore()

const loading = ref(false)
const starting = ref(false)
const cancelling = ref(false)
const savingWatch = ref(false)
const error = ref('')

const probes = ref<EvalProbe[]>([])
const suites = ref<EvalSuite[]>([])
const channelsByKind = ref<Record<ApiTab, Channel[]>>(emptyByKind<Channel>())
const poolsByKind = ref<Record<ApiTab, ChannelPool[]>>(emptyByKind<ChannelPool>())

const selectedSuiteId = ref('')
const selectedChannelIds = ref<string[]>([])
const enabledProtocols = ref<ApiTab[]>([...EVAL_PROTOCOL_KINDS])
const thinking = ref('inherit')
const modelOverride = ref('')
const channelModels = ref<Record<string, string>>({})

const busy = ref(false)
const currentRun = ref<EvalRun | null>(null)
const runHistory = ref<EvalRun[]>([])
const historyDrawer = ref(false)
const probeDrawer = ref(false)
const cellDrawer = ref(false)
const cellSelection = ref<EvalCellSelection | null>(null)

const optionsMenu = ref(false)
const watchMenu = ref(false)
const watchConfig = ref<EvalWatchConfig>({
  enabled: false,
  suiteId: '',
  interval: '2h',
  channelIds: [],
  lastRunAt: 0,
  nextRunAt: 0
})
const watchForm = ref({ enabled: false, suiteId: '', interval: '2h' })

let runStream: AbortController | null = null

const suiteItems = computed(() =>
  suites.value.map(suite => ({
    title: suite.cheap ? `${suite.name} · 便宜` : suite.name,
    value: suite.id,
    props: { 'data-description': suite.description || '' } as Record<string, string>
  }))
)
const selectedSuiteDescription = computed(() => {
  const suite = suites.value.find(item => item.id === selectedSuiteId.value)
  return suite?.description || ''
})
const cheapSuiteItems = computed(() =>
  suites.value.filter(suite => suite.cheap).map(suite => ({ title: suite.name, value: suite.id }))
)

const watchButtonLabel = computed(() => {
  if (!watchConfig.value.enabled) return '定时'
  return `定时 · ${evalIntervalLabel(watchConfig.value.interval)}`
})

/** 矩阵的列取自当前批次的套件；没跑过就用配置条里选的套件预览列。 */
const matrixProbes = computed(() => {
  const suiteId = currentRun.value?.suiteId || selectedSuiteId.value
  const suite = suites.value.find(item => item.id === suiteId)
  if (!suite) return []
  return suite.probeIds
    .map(id => probes.value.find(probe => probe.id === id))
    .filter((probe): probe is EvalProbe => !!probe)
})

const canStart = computed(() => !!selectedSuiteId.value && selectedChannelIds.value.length > 0 && !busy.value)

const channelNameOf = (channelId: string) => {
  for (const kind of EVAL_PROTOCOL_KINDS) {
    const found = (channelsByKind.value[kind] || []).find(channel => channel.id === channelId)
    if (found) return found.name
  }
  return channelId
}

const openCell = (selection: EvalCellSelection) => {
  cellSelection.value = selection
  cellDrawer.value = true
}

const stopRunStream = () => {
  runStream?.abort()
  runStream = null
}

/** 订阅批次进度。后端只在有变化时发帧，跑完自动断开。 */
const subscribeRun = (runId: string) => {
  stopRunStream()
  const controller = new AbortController()
  runStream = controller
  api
    .streamEvalRun(
      runId,
      payload => {
        if (payload.error) {
          error.value = payload.error
          return
        }
        if (!payload.run) return
        currentRun.value = payload.run
        busy.value = payload.run.status === 'running' || payload.run.status === 'queued'
      },
      { signal: controller.signal }
    )
    .catch(err => {
      if (controller.signal.aborted) return
      error.value = err instanceof Error ? err.message : String(err)
    })
    .finally(() => {
      if (runStream === controller) {
        runStream = null
        busy.value = false
      }
    })
}

const loadChannels = async () => {
  await Promise.all(
    EVAL_PROTOCOL_KINDS.map(async kind => {
      const [channelResponse, poolResponse] = await Promise.all([
        channelApiByType(kind).getChannels(),
        api.getChannelPools(kind)
      ])
      channelsByKind.value[kind] = channelResponse.channels || []
      poolsByKind.value[kind] = poolResponse.pools || []
    })
  )
}

/** 从渠道菜单跳进来时只亮那个协议并勾上它；否则恢复上次勾选。 */
const applyDeepLinkOrMemory = () => {
  const deepLink = typeof route.query.channel === 'string' ? route.query.channel : ''
  if (deepLink) {
    selectedChannelIds.value = [deepLink]
    const kind = EVAL_PROTOCOL_KINDS.find(item =>
      (channelsByKind.value[item] || []).some(channel => channel.id === deepLink)
    )
    if (kind) {
      enabledProtocols.value = [kind]
    }
    return
  }
  if (preferences.evalLastChannelIds.length) {
    selectedChannelIds.value = [...preferences.evalLastChannelIds]
  }
  if (preferences.evalLastSuiteId) {
    selectedSuiteId.value = preferences.evalLastSuiteId
  }
}

const reloadProbes = async () => {
  const [probeResponse, suiteResponse] = await Promise.all([api.listEvalProbes(), api.listEvalSuites()])
  probes.value = probeResponse.probes || []
  suites.value = suiteResponse.suites || []
}

const reloadAll = async () => {
  loading.value = true
  error.value = ''
  try {
    const [watchResponse, runResponse] = await Promise.all([
      api.getEvalWatch(),
      api.listEvalRuns(),
      reloadProbes(),
      loadChannels()
    ])
    watchConfig.value = watchResponse.watch
    watchForm.value = {
      enabled: watchConfig.value.enabled,
      suiteId: watchConfig.value.suiteId,
      interval: watchConfig.value.interval
    }
    busy.value = runResponse.busy

    if (!selectedSuiteId.value && suites.value.length) {
      selectedSuiteId.value = suites.value.find(suite => suite.cheap)?.id || suites.value[0].id
    }
    applyDeepLinkOrMemory()

    // 评测页面的模型、思考和渠道选择同时作为值班配置的编辑入口。
    // 刷新后必须恢复值班实际使用的完整配置，否则用户再次保存时会把旧值覆盖掉。
    const deepLink = typeof route.query.channel === 'string' ? route.query.channel : ''
    if (!deepLink && watchConfig.value.channelIds?.length) {
      selectedChannelIds.value = [...watchConfig.value.channelIds]
    }
    modelOverride.value = watchConfig.value.model || ''
    channelModels.value = { ...(watchConfig.value.channelModels || {}) }
    thinking.value = watchConfig.value.thinking || 'inherit'

    const runId = runResponse.currentRunId || runResponse.runs?.[0]?.id
    runHistory.value = runResponse.runs || []
    if (runId) {
      const detail = await api.getEvalRun(runId)
      currentRun.value = detail.run
      if (runResponse.busy) {
        subscribeRun(runId)
      }
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

/** 从历史抽屉点开某次批次，把它展示到矩阵。不再自动跳回最新。 */
const selectRun = async (runId: string) => {
  if (!runId) return
  error.value = ''
  try {
    const detail = await api.getEvalRun(runId)
    currentRun.value = detail.run
    historyDrawer.value = false
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

const startRun = async () => {
  starting.value = true
  error.value = ''
  try {
    const response = await api.startEvalRun({
      suiteId: selectedSuiteId.value,
      channelIds: selectedChannelIds.value,
      model: modelOverride.value,
      channelModels: channelModels.value,
      thinking: thinking.value
    })
    currentRun.value = response.run
    busy.value = true
    subscribeRun(response.run.id)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    starting.value = false
  }
}

const cancelRun = async () => {
  if (!currentRun.value) return
  cancelling.value = true
  try {
    await api.cancelEvalRun(currentRun.value.id)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    cancelling.value = false
  }
}

const saveWatch = async () => {
  savingWatch.value = true
  error.value = ''
  try {
    const response = await api.putEvalWatch({
      enabled: watchForm.value.enabled,
      suiteId: watchForm.value.suiteId,
      interval: watchForm.value.interval,
      channelIds: selectedChannelIds.value,
      model: modelOverride.value,
      channelModels: channelModels.value,
      thinking: thinking.value
    })
    watchConfig.value = response.watch
    watchMenu.value = false
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    savingWatch.value = false
  }
}

watch([selectedChannelIds, selectedSuiteId], () => {
  preferences.setEvalLastSelection(selectedChannelIds.value, selectedSuiteId.value)
})

onMounted(() => {
  void reloadAll()
})

onUnmounted(() => {
  stopRunStream()
})
</script>

<style scoped>
.eval-page {
  max-width: 1440px;
  margin: 0 auto;
}

.page-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

@media (max-width: 600px) {
  .page-heading {
    align-items: flex-start;
    flex-direction: column;
  }
}

.run-history-item {
  cursor: pointer;
}

.run-history-item:hover {
  background: rgba(var(--v-theme-on-surface), 0.06);
}
</style>
