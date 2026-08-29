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
      <div class="d-flex flex-wrap align-center ga-3">
        <div class="suite-select">
          <v-select
            v-model="selectedSuiteId"
            :items="suiteItems"
            item-title="title"
            item-value="value"
            label="套件"
            density="compact"
            variant="outlined"
            hide-details
            style="min-width: 220px"
          >
            <template #selection="{ item }">
              <span class="text-truncate">{{ item.raw.title }}</span>
            </template>
            <template #item="{ props: itemProps, item }">
              <v-list-item v-bind="itemProps" :title="item.raw.title">
                <template #append>
                  <v-tooltip :text="item.raw.description || '暂无说明'" location="start">
                    <template #activator="{ props: tip }">
                      <v-icon v-bind="tip" icon="mdi-help-circle-outline" size="small" class="text-medium-emphasis" />
                    </template>
                  </v-tooltip>
                </template>
              </v-list-item>
            </template>
          </v-select>
          <v-tooltip v-if="selectedSuiteDescription" :text="selectedSuiteDescription" location="bottom">
            <template #activator="{ props: tip }">
              <v-icon v-bind="tip" icon="mdi-help-circle-outline" size="small" class="ml-1 text-medium-emphasis" />
            </template>
          </v-tooltip>
        </div>

        <span class="text-body-2 text-medium-emphasis">已选 {{ selectedChannelIds.length }} 渠道</span>
        <v-spacer />

        <v-select
          v-model="thinking"
          :items="EVAL_THINKING_ITEMS"
          item-title="title"
          item-value="value"
          label="思考等级"
          density="compact"
          variant="outlined"
          hide-details
          style="min-width: 140px"
        />

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

      <div class="mt-3 d-flex align-center ga-2 probe-select-bar">
        <span class="text-body-2 text-medium-emphasis">题目</span>
        <v-chip
          v-for="probe in probeCandidates"
          :key="probe.id"
          size="small"
          :color="probe.candidate ? 'primary' : 'grey'"
          :variant="probe.candidate ? 'flat' : 'outlined'"
          @click="toggleProbe(probe.id)"
        >
          {{ probe.name }}
        </v-chip>
        <v-tooltip :text="probeModeHint" location="top">
          <template #activator="{ props: tip }">
            <v-icon v-bind="tip" icon="mdi-help-circle-outline" size="small" class="text-medium-emphasis" />
          </template>
        </v-tooltip>
        <v-btn size="small" variant="text" prepend-icon="mdi-format-list-bulleted" @click="probeDrawer = true">
          管理题目
        </v-btn>
      </div>

      <div class="mt-4">
        <EvalChannelPicker
          v-model="selectedChannelIds"
          v-model:protocols="enabledProtocols"
          v-model:channel-models="channelModels"
          v-model:channel-thinking="channelThinking"
          :channels-by-kind="channelsByKind"
          :pools-by-kind="poolsByKind"
        />
      </div>
    </v-card>

    <v-card
      v-if="currentRun"
      :id="`run-card-${currentRun.id}`"
      class="pa-4 mb-4 elevation-0 border run-card"
      :class="{ 'run-card--flash': flashRunId === currentRun.id }"
    >
      <div class="d-flex flex-wrap align-center ga-2 mb-2">
        <div class="text-h6 font-weight-bold">{{ currentRun.suiteName || '未知套件' }}</div>
        <v-chip size="small" variant="tonal" :color="evalRunStatusColor(currentRun.status)">
          {{ evalRunStatusLabel(currentRun.status) }}
        </v-chip>
        <v-chip v-if="currentRun.trigger === 'watch'" size="small" variant="outlined" color="grey">定时</v-chip>
        <v-chip v-if="currentRun.thinking && currentRun.thinking !== 'inherit'" size="small" variant="outlined" color="grey">
          思考 {{ evalThinkingLabel(currentRun.thinking) }}
        </v-chip>
        <v-spacer />
        <v-btn variant="text" size="small" prepend-icon="mdi-history" @click="historyDrawer = true">历史</v-btn>
      </div>

      <div class="text-caption text-medium-emphasis mb-3 run-overview-meta">
        <span><v-icon size="12" icon="mdi-account-group-outline" /> {{ currentRun.channelIds.length }} 渠道</span>
        <span><v-icon size="12" icon="mdi-format-list-bulleted" /> {{ matrixProbes.length }} 题</span>
        <span><v-icon size="12" icon="mdi-timer-sand" /> {{ evalDurationLabel(currentRun.startedAt, currentRun.finishedAt) }}</span>
        <span><v-icon size="12" icon="mdi-clock-outline" /> {{ evalFormatTime(currentRun.startedAt) }}</span>
      </div>

      <div v-if="tallySegments.length" class="tally-bar mb-3">
        <div
          v-for="segment in tallySegments"
          :key="segment.key"
          class="tally-segment"
          :class="`tally-segment--${segment.color}`"
          :style="{ flex: segment.ratio }"
          :title="`${segment.label} ${segment.count} (${Math.round(segment.ratio * 100)}%)`"
        >
          <span v-if="segment.ratio > 0.08" class="tally-segment-label">{{ segment.count }}</span>
        </div>
      </div>

      <div v-if="tallySegments.length" class="tally-legend mb-3">
        <span v-for="segment in tallySegments" :key="segment.key" class="tally-legend-item">
          <span class="tally-dot" :class="`tally-dot--${segment.color}`" />
          {{ segment.label }} {{ segment.count }}
        </span>
      </div>

      <v-alert v-if="currentRun?.error" type="warning" variant="tonal" density="compact" class="mb-3">
        {{ currentRun.error }}
      </v-alert>

      <EvalMatrix :run="currentRun" :probes="matrixProbes" :channels-by-kind="channelsByKind" @cell="openCell" />
    </v-card>

    <!-- 最近批次缩略条：开始新任务后，上一个批次不会消失，从这里快速切回。 -->
    <v-card v-if="recentRuns.length" class="mb-4 pa-3 elevation-0 border recent-runs" elevation="0" border>
      <div class="d-flex align-center ga-2 mb-2">
        <div class="text-body-2 font-weight-medium">最近批次</div>
        <v-spacer />
        <v-btn size="x-small" variant="text" prepend-icon="mdi-history" @click="historyDrawer = true">全部历史</v-btn>
      </div>
      <div class="recent-runs-list">
        <div
          v-for="item in recentRuns"
          :key="item.id"
          class="recent-run-card"
          role="button"
          tabindex="0"
          @click="selectRun(item.id)"
          @keydown.enter.prevent="selectRun(item.id)"
        >
          <div class="d-flex align-center ga-2">
            <v-chip size="x-small" variant="tonal" :color="evalRunStatusColor(item.status)">
              {{ evalRunStatusLabel(item.status) }}
            </v-chip>
            <span class="text-truncate font-weight-medium recent-run-title">{{ item.suiteName || '未知套件' }}</span>
          </div>
          <div v-if="historyTallySegments(item).length" class="history-tally-bar">
            <div
              v-for="segment in historyTallySegments(item)"
              :key="segment.key"
              class="history-tally-seg"
              :class="`history-tally-seg--${segment.color}`"
              :style="{ flex: segment.ratio }"
              :title="`${segment.label} ${segment.count}`"
            />
          </div>
          <div class="history-card-time">{{ recentRunStatusLabel(item) }}</div>
        </div>
      </div>
    </v-card>

    <v-navigation-drawer v-model="historyDrawer" location="end" temporary width="520">
      <div class="pa-4">
        <div class="d-flex align-center ga-2 mb-4">
          <v-icon icon="mdi-history" size="small" class="text-medium-emphasis" />
          <div class="text-h6">评测历史</div>
          <v-spacer />
          <v-btn icon="mdi-close" size="small" variant="text" @click="historyDrawer = false" />
        </div>

        <div v-if="deepLinkChannelId" class="history-scope-banner mb-3">
          <v-icon icon="mdi-filter-variant" size="18" color="primary" />
          <div class="history-scope-copy">
            <div class="text-body-2 font-weight-medium">渠道历史评测</div>
            <div class="text-caption text-medium-emphasis">
              {{ channelNameOf(deepLinkChannelId) }} · {{ visibleHistoryRuns.length }} 条记录
            </div>
          </div>
          <v-chip size="x-small" color="primary" variant="tonal">已定位</v-chip>
        </div>

        <div v-if="!visibleHistoryRuns.length" class="text-body-2 text-medium-emphasis py-8 text-center">
          {{ deepLinkChannelId ? '该渠道还没有评测记录' : '还没有评测记录' }}
        </div>

        <div v-else class="history-list">
          <div
            v-for="item in visibleHistoryRuns"
            :key="item.id"
            :id="`history-run-card-${item.id}`"
            class="history-card"
            :class="{
              'history-card--active': currentRun?.id === item.id,
              'history-card--channel-focus': !!deepLinkChannelId
            }"
            role="button"
            tabindex="0"
            :aria-label="`${item.suiteName || '未知套件'}，${evalRunStatusLabel(item.status)}，${evalFormatTime(item.startedAt || item.createdAt)}`"
            @click="selectRun(item.id)"
            @keydown.enter.prevent="selectRun(item.id)"
            @keydown.space.prevent="selectRun(item.id)"
          >
            <div class="history-card-header">
              <div class="text-truncate font-weight-medium">{{ item.suiteName || '未知套件' }}</div>
              <v-chip size="x-small" variant="tonal" :color="evalRunStatusColor(item.status)">
                {{ evalRunStatusLabel(item.status) }}
              </v-chip>
            </div>

            <div class="history-card-meta">
              <span class="meta-item">
                <v-icon size="12" icon="mdi-account-group-outline" />
                {{ item.channelIds.length }} 渠道
              </span>
              <span v-if="item.trigger === 'watch'" class="meta-item">
                <v-icon size="12" icon="mdi-clock-outline" />定时
              </span>
              <span class="meta-item">
                <v-icon size="12" icon="mdi-timer-sand" />
                {{ evalDurationLabel(item.startedAt, item.finishedAt) }}
              </span>
            </div>

            <div v-if="historyTallySegments(item).length" class="history-tally-bar">
              <div
                v-for="segment in historyTallySegments(item)"
                :key="segment.key"
                class="history-tally-seg"
                :class="`history-tally-seg--${segment.color}`"
                :style="{ flex: segment.ratio }"
                :title="`${segment.label} ${segment.count}`"
              />
            </div>

            <div class="history-card-time">{{ evalFormatTime(item.startedAt || item.createdAt) }}</div>
          </div>
        </div>
      </div>
    </v-navigation-drawer>

    <EvalResultDrawer
      v-model="cellDrawer"
      :selection="cellSelection"
      :channel-name-of="channelNameOf"
      :probes="matrixProbes"
      :results="currentRun?.results"
      @navigate="onCellNavigate"
    />
    <EvalProbeManager v-model="probeDrawer" :probes="probes" @changed="reloadProbes" />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
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
  EVAL_THINKING_ITEMS,
  evalDurationLabel,
  evalFormatTime,
  evalIntervalLabel,
  evalRunStatusColor,
  evalRunStatusLabel,
  evalThinkingLabel,
  tallyToSegments,
  tallyVerdicts,
  type EvalCellSelection,
  type EvalVerdictTally
} from '@/utils/eval'

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

/** 从渠道条跳转时，使用稳定 channel.id 筛出该渠道的全部历史批次。 */
const deepLinkChannelId = computed(() => {
  const value = route.query.channel
  return typeof value === 'string' ? value.trim() : ''
})

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
const channelThinking = ref<Record<string, string>>({})
/** 取消勾选的题（基于当前套件）。跑历史批次时忽略——它用的是当时的题。 */
const excludedProbeIds = ref<string[]>([])

const busy = ref(false)
const currentRun = ref<EvalRun | null>(null)
const runHistory = ref<EvalRun[]>([])
const historyDrawer = ref(false)
const probeDrawer = ref(false)
const cellDrawer = ref(false)
const cellSelection = ref<EvalCellSelection | null>(null)
/** 点击历史卡片后短暂高亮当前批次卡片，提示已定位。 */
const flashRunId = ref('')

const visibleHistoryRuns = computed(() => {
  const channelId = deepLinkChannelId.value
  if (!channelId) return runHistory.value
  return runHistory.value.filter(run => run.channelIds.includes(channelId))
})

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
    description: suite.description || '',
    value: suite.id
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

/**
 * 配置条里可选的题目：当前套件的题，标注 candidate。
 * 跑历史批次时仍显示当前套件的题作为列（历史结果与当前题对不上的格子显示"待跑"，
 * 这是可接受的近似——历史详情在抽屉里仍能看到真实结果）。
 */
const matrixProbes = computed(() => {
  const suiteId = currentRun.value?.suiteId || selectedSuiteId.value
  const suite = suites.value.find(item => item.id === suiteId)
  if (!suite) return []
  return suite.probeIds
    .map(id => probes.value.find(probe => probe.id === id))
    .filter((probe): probe is EvalProbe => !!probe)
})

/** 配置条显示的题带 candidate 标记：true=已选，false=已从套件里取消勾选。 */
const probeCandidates = computed(() =>
  matrixProbes.value.map(probe => ({
    ...probe,
    candidate: !excludedProbeIds.value.includes(probe.id)
  }))
)

/** 本次要跑的题：套件里未取消勾选的。 */
const activeProbeIds = computed(() =>
  matrixProbes.value.map(probe => probe.id).filter(id => !excludedProbeIds.value.includes(id))
)

const probeModeHint = computed(() => {
  if (excludedProbeIds.value.length === 0) return '点击题目可取消勾选；只跑勾中的题。'
  return `已取消 ${excludedProbeIds.value.length} 题，本次只跑 ${activeProbeIds.value.length} 题。`
})

/** 只影响新发起的批次；正在跑的历史批次不受影响。 */
const toggleProbe = (probeId: string) => {
  if (excludedProbeIds.value.includes(probeId)) {
    excludedProbeIds.value = excludedProbeIds.value.filter(id => id !== probeId)
    return
  }
  excludedProbeIds.value = [...excludedProbeIds.value, probeId]
}

const canStart = computed(
  () => !!selectedSuiteId.value && selectedChannelIds.value.length > 0 && activeProbeIds.value.length > 0 && !busy.value
)

/** 当前批次的 verdict 分布，用于概览条。total = 渠道数 × 探针数。 */
const currentTally = computed<EvalVerdictTally | null>(() => {
  if (!currentRun.value) return null
  // 后端已算好 tally 就直接用（GetRun/ListRuns 都带）；没有时退回前端现算。
  if (currentRun.value.tally) {
    return {
      pass: currentRun.value.tally.pass,
      suspect: currentRun.value.tally.suspect,
      fail: currentRun.value.tally.fail,
      error: currentRun.value.tally.error,
      insufficient: currentRun.value.tally.insufficient,
      inapplicable: currentRun.value.tally.inapplicable,
      pending: 0,
      total: currentRun.value.tally.total
    }
  }
  const cellCount = currentRun.value.channelIds.length * matrixProbes.value.length
  return tallyVerdicts(currentRun.value.results, cellCount)
})

const tallySegments = computed(() => tallyToSegments(currentTally.value))

/** 历史卡片用后端已聚合的 tally 画 mini 条；tally 缺失（极老批次）就空，不画。 */
const historyTallySegments = (run: EvalRun) => {
  if (!run.tally) return []
  return tallyToSegments({
    pass: run.tally.pass,
    suspect: run.tally.suspect,
    fail: run.tally.fail,
    error: run.tally.error,
    insufficient: run.tally.insufficient,
    inapplicable: run.tally.inapplicable,
    pending: 0,
    total: run.tally.total
  })
}

/** 最近批次缩略条：历史里最新的 4 个（不含正在显示的当前批次）。 */
const recentRuns = computed(() => {
  if (!currentRun.value) return []
  return runHistory.value
    .filter(item => item.id !== currentRun.value?.id)
    .slice(0, 4)
})

const recentRunStatusLabel = (run: EvalRun) => {
  const finished = run.finishedAt || 0
  const now = Math.floor(Date.now() / 1000)
  if (finished && now - finished < 5) return '刚刚'
  return evalFormatTime(finished || run.startedAt || run.createdAt)
}

const channelNameOf = (channelId: string) => {
  for (const kind of EVAL_PROTOCOL_KINDS) {
    const found = (channelsByKind.value[kind] || []).find(channel => channel.id === channelId)
    if (found) return found.name
  }
  return channelId
}

/** 深链进入评测页时打开历史抽屉，并把最新一条匹配记录滚入可视区。 */
const focusDeepLinkedHistory = async () => {
  const channelId = deepLinkChannelId.value
  if (!channelId) return

  historyDrawer.value = true
  await nextTick()
  const firstMatchedRun = visibleHistoryRuns.value[0]
  if (!firstMatchedRun) return
  document
    .getElementById(`history-run-card-${firstMatchedRun.id}`)
    ?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
}

const openCell = (selection: EvalCellSelection) => {
  cellSelection.value = selection
  cellDrawer.value = true
}

/** 抽屉内切换探针后，同步选中态，保持抽屉打开。 */
const onCellNavigate = (selection: EvalCellSelection) => {
  cellSelection.value = selection
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
    channelThinking.value = { ...(watchConfig.value.channelThinking || {}) }
    thinking.value = watchConfig.value.thinking || 'inherit'

    runHistory.value = runResponse.runs || []
    const deepLinkRun = deepLinkChannelId.value
      ? runHistory.value.find(run => run.channelIds.includes(deepLinkChannelId.value))
      : undefined
    const runId = deepLinkRun?.id || runResponse.currentRunId || runHistory.value[0]?.id
    if (runId) {
      const detail = await api.getEvalRun(runId)
      currentRun.value = detail.run
      if (runResponse.busy && runResponse.currentRunId === runId) {
        subscribeRun(runResponse.currentRunId)
      }
    }
    await focusDeepLinkedHistory()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

/** 从历史抽屉点开某次批次，把它展示到矩阵，并滚动定位到批次卡片 + 短暂高亮。 */
const selectRun = async (runId: string) => {
  if (!runId) return
  error.value = ''
  try {
    const detail = await api.getEvalRun(runId)
    currentRun.value = detail.run
    historyDrawer.value = false
    flashRunId.value = runId
    await nextTick()
    document
      .getElementById(`run-card-${runId}`)
      ?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    window.setTimeout(() => {
      if (flashRunId.value === runId) flashRunId.value = ''
    }, 1400)
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
      channelThinking: channelThinking.value,
      thinking: thinking.value,
      probeIds: activeProbeIds.value
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
      channelThinking: channelThinking.value,
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

/* 批次概览元信息行 */
.run-overview-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
}

.run-overview-meta span {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

/* verdict 分布条 */
.tally-bar {
  display: flex;
  height: 22px;
  border-radius: 6px;
  overflow: hidden;
  background: rgba(var(--v-theme-on-surface), 0.08);
}

.tally-segment {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 2px;
  transition: flex-grow 0.2s;
}

.tally-segment-label {
  font-size: 11px;
  font-weight: 600;
  color: rgba(255, 255, 255, 0.92);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
}

.tally-segment--success { background: rgb(var(--v-theme-success)); }
.tally-segment--warning { background: rgb(var(--v-theme-warning)); }
.tally-segment--error   { background: rgb(var(--v-theme-error)); }
.tally-segment--grey    { background: rgba(var(--v-theme-on-surface), 0.35); }

/* 分布条下方图例 */
.tally-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 12px 18px;
  font-size: 0.75rem;
  color: rgba(var(--v-theme-on-surface), 0.7);
}

.tally-legend-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.tally-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.tally-dot--success { background: rgb(var(--v-theme-success)); }
.tally-dot--warning { background: rgb(var(--v-theme-warning)); }
.tally-dot--error   { background: rgb(var(--v-theme-error)); }
.tally-dot--grey    { background: rgba(var(--v-theme-on-surface), 0.35); }

/* 历史抽屉卡片列表 */
.history-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.history-scope-banner {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 10px 12px;
  border: 1px solid rgba(var(--v-theme-primary), 0.38);
  border-left: 3px solid rgb(var(--v-theme-primary));
  border-radius: 6px 2px 6px 2px;
  background: rgba(var(--v-theme-primary), 0.08);
}

.history-scope-copy { min-width: 0; flex: 1; }
.history-scope-copy > div { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.history-card {
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}

.history-card:hover {
  border-color: rgba(var(--v-theme-primary), 0.4);
  background: rgba(var(--v-theme-primary), 0.04);
}

.history-card--active {
  border-color: rgb(var(--v-theme-primary));
  background: rgba(var(--v-theme-primary), 0.08);
}

.history-card--channel-focus {
  border-left: 3px solid rgba(var(--v-theme-primary), 0.72);
  padding-left: 10px;
}

.history-card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.history-card-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 0.75rem;
  color: rgba(var(--v-theme-on-surface), 0.6);
  margin-bottom: 2px;
}

.meta-item {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

.history-card-time {
  font-size: 0.6875rem;
  color: rgba(var(--v-theme-on-surface), 0.45);
}

/* 历史卡片 mini 统计条 */
.history-tally-bar {
  display: flex;
  height: 4px;
  border-radius: 2px;
  overflow: hidden;
  background: rgba(var(--v-theme-on-surface), 0.08);
  margin-top: 4px;
}

.history-tally-seg {
  min-width: 1px;
}

.history-tally-seg--success { background: rgb(var(--v-theme-success)); }
.history-tally-seg--warning { background: rgb(var(--v-theme-warning)); }
.history-tally-seg--error   { background: rgb(var(--v-theme-error)); }
.history-tally-seg--grey    { background: rgba(var(--v-theme-on-surface), 0.35); }

/* 套件下拉框 + 问号说明 */
.suite-select {
  display: flex;
  align-items: center;
  gap: 2px;
}

/* 题目选择条 */
.probe-select-bar {
  flex-wrap: wrap;
  row-gap: 6px;
}

.probe-select-bar .v-chip {
  cursor: pointer;
}

/* 当前批次卡片高亮（点击历史后定位提示） */
.run-card {
  transition: border-color 0.3s, box-shadow 0.3s;
}

.run-card--flash {
  border-color: rgb(var(--v-theme-primary)) !important;
  box-shadow: 0 0 0 2px rgba(var(--v-theme-primary), 0.25);
}

/* 最近批次缩略条 */
.recent-runs-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 8px;
}

.recent-run-card {
  padding: 8px 10px;
  border-radius: 8px;
  border: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}

.recent-run-card:hover {
  border-color: rgba(var(--v-theme-primary), 0.4);
  background: rgba(var(--v-theme-primary), 0.04);
}

.recent-run-title {
  font-size: 0.8125rem;
}
</style>
