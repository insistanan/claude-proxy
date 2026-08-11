<template>
  <div class="audit-jobs-view">
    <div class="audit-page-header">
      <div>
        <h1 class="text-h5 font-weight-bold mb-1">模型审计</h1>
        <div class="text-body-2 text-medium-emphasis">共 {{ totalJobs }} 个任务</div>
      </div>
      <div class="audit-page-actions">
        <v-btn
          variant="text"
          prepend-icon="mdi-refresh"
          :loading="loading"
          :disabled="saving"
          @click="loadJobs"
        >
          刷新
        </v-btn>
        <v-btn
          color="primary"
          prepend-icon="mdi-plus"
          :disabled="catalogLoading || !catalogReady"
          @click="openCreateDialog"
        >
          新建任务
        </v-btn>
      </div>
    </div>

    <v-alert v-if="pageError" type="error" variant="tonal" closable class="mb-4" @click:close="pageError = ''">
      {{ pageError }}
    </v-alert>
    <v-alert v-else-if="catalogNotice" type="warning" variant="tonal" class="mb-4">
      {{ catalogNotice }}
    </v-alert>

    <div class="audit-list" :aria-busy="loading">
      <div class="audit-list-header" aria-hidden="true">
        <span>任务</span>
        <span>工作负载</span>
        <span>目标</span>
        <span>调度</span>
        <span>预算</span>
        <span class="text-right">操作</span>
      </div>

      <v-skeleton-loader v-if="loading && jobs.length === 0" type="list-item-three-line@4" />

      <div v-else-if="jobs.length === 0" class="audit-empty-state">
        <v-icon size="42" color="medium-emphasis">mdi-test-tube</v-icon>
        <div class="text-subtitle-1 font-weight-medium mt-3">暂无审计任务</div>
        <v-btn
          color="primary"
          variant="tonal"
          class="mt-4"
          prepend-icon="mdi-plus"
          :disabled="catalogLoading || !catalogReady"
          @click="openCreateDialog"
        >
          创建首个任务
        </v-btn>
      </div>

      <div v-for="job in jobs" :key="job.id" class="audit-list-row">
        <div class="audit-job-main" data-label="任务">
          <div class="audit-job-title-line">
            <span class="font-weight-semibold audit-job-name">{{ job.name }}</span>
            <v-chip :color="statusMeta(job.status).color" size="small" variant="tonal">
              {{ statusMeta(job.status).label }}
            </v-chip>
          </div>
          <div class="text-caption text-medium-emphasis mt-1">{{ job.id }} · r{{ job.revision }}</div>
        </div>

        <div data-label="工作负载">
          <div class="text-body-2 font-weight-medium">{{ workloadLabel(job) }}</div>
          <div class="text-caption text-medium-emphasis mt-1">{{ workloadDetail(job) }}</div>
        </div>

        <div data-label="目标">
          <div class="text-body-2">{{ job.targets.length }} 个渠道</div>
          <div class="text-caption text-medium-emphasis mt-1 audit-target-summary">{{ targetSummary(job) }}</div>
        </div>

        <div data-label="调度">
          <div class="text-body-2">{{ formatInterval(job.schedule.intervalMs) }}</div>
          <div class="text-caption text-medium-emphasis mt-1">{{ formatDateTime(job.schedule.startAt) }} 起</div>
        </div>

        <div data-label="预算">
          <div class="text-body-2">{{ job.budget.maxRequests }} 请求</div>
          <div class="text-caption text-medium-emphasis mt-1">{{ formatCompactNumber(job.budget.maxTotalTokens) }} tokens</div>
        </div>

        <div class="audit-row-actions" data-label="操作">
          <v-tooltip text="立即运行" location="bottom">
            <template #activator="{ props }">
              <v-btn
                v-bind="props"
                icon="mdi-play-circle"
                variant="text"
                class="audit-icon-btn"
                :loading="actionKey === `${job.id}:run`"
                :disabled="job.status !== 'enabled' || actionBusy"
                :aria-label="`立即运行 ${job.name}`"
                @click="startRun(job)"
              />
            </template>
          </v-tooltip>

          <v-tooltip text="运行记录" location="bottom">
            <template #activator="{ props }">
              <v-btn
                v-bind="props"
                icon="mdi-format-list-bulleted"
                variant="text"
                class="audit-icon-btn"
                :disabled="actionBusy"
                :aria-label="`查看 ${job.name} 的运行记录`"
                @click="openRunsDialog(job)"
              />
            </template>
          </v-tooltip>

          <v-tooltip text="编辑" location="bottom">
            <template #activator="{ props }">
              <v-btn
                v-bind="props"
                icon="mdi-pencil"
                variant="text"
                class="audit-icon-btn"
                :disabled="!canEdit(job) || actionBusy"
                :aria-label="`编辑 ${job.name}`"
                @click="openEditDialog(job)"
              />
            </template>
          </v-tooltip>

          <v-tooltip :text="job.status === 'enabled' ? '暂停' : '启用'" location="bottom">
            <template #activator="{ props }">
              <v-btn
                v-bind="props"
                :icon="job.status === 'enabled' ? 'mdi-pause-circle' : 'mdi-check-circle'"
                variant="text"
                class="audit-icon-btn"
                :color="job.status === 'enabled' ? 'warning' : 'success'"
                :loading="actionKey === `${job.id}:transition`"
                :disabled="!canToggle(job) || actionBusy"
                :aria-label="`${job.status === 'enabled' ? '暂停' : '启用'} ${job.name}`"
                @click="toggleJob(job)"
              />
            </template>
          </v-tooltip>

          <v-tooltip text="删除" location="bottom">
            <template #activator="{ props }">
              <v-btn
                v-bind="props"
                icon="mdi-delete"
                color="error"
                variant="text"
                class="audit-icon-btn"
                :disabled="job.status === 'running' || actionBusy"
                :aria-label="`删除 ${job.name}`"
                @click="confirmDelete(job)"
              />
            </template>
          </v-tooltip>
        </div>
      </div>
    </div>

    <div v-if="pageCount > 1" class="d-flex justify-center mt-5">
      <v-pagination v-model="page" :length="pageCount" :disabled="loading" @update:model-value="loadJobs" />
    </div>

    <v-dialog v-model="formDialog" max-width="1040" persistent scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>{{ editingJob ? '编辑审计任务' : '新建审计任务' }}</span>
          <v-btn
            icon="mdi-close"
            variant="text"
            class="audit-icon-btn"
            aria-label="关闭"
            :disabled="saving"
            @click="closeFormDialog"
          />
        </v-card-title>
        <v-divider />

        <v-card-text class="audit-form-body">
          <v-alert v-if="formError" type="error" variant="tonal" class="mb-5">{{ formError }}</v-alert>

          <section class="audit-form-section">
            <div class="audit-section-heading">基本信息</div>
            <div class="audit-form-grid audit-form-grid--two">
              <v-text-field
                v-model="form.name"
                label="任务名称"
                variant="outlined"
                density="comfortable"
                maxlength="120"
                counter
                required
              />
              <v-select
                v-if="!editingJob"
                v-model="form.initialStatus"
                label="创建状态"
                :items="initialStatusOptions"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-else
                :model-value="statusMeta(editingJob.status).label"
                label="当前状态"
                variant="outlined"
                density="comfortable"
                readonly
              />
            </div>
          </section>

          <v-divider class="my-5" />

          <section class="audit-form-section">
            <div class="audit-section-heading">审计内容</div>
            <v-btn-toggle
              v-model="form.workloadKind"
              mandatory
              color="primary"
              variant="outlined"
              class="mb-4"
              aria-label="审计内容类型"
            >
              <v-btn value="identity">身份与混用</v-btn>
              <v-btn value="capability">通用能力</v-btn>
            </v-btn-toggle>

            <div v-if="form.workloadKind === 'identity'" class="audit-strategy-list">
              <div v-for="strategy in strategies" :key="strategy.ref.id" class="audit-strategy-row">
                <v-checkbox
                  :model-value="strategyState(strategy.ref.id).enabled"
                  color="primary"
                  hide-details
                  :aria-label="`启用 ${strategy.ref.id}`"
                  @update:model-value="setStrategyEnabled(strategy.ref.id, Boolean($event))"
                />
                <div class="audit-strategy-content">
                  <div class="d-flex flex-wrap align-center ga-2">
                    <span class="text-body-2 font-weight-medium">{{ strategyName(strategy.ref.id) }}</span>
                    <v-chip v-if="strategy.formalEligible" size="x-small" color="success" variant="tonal">正式资格</v-chip>
                    <v-chip v-if="strategy.applicability.models?.length" size="x-small" variant="outlined">Sol 专项</v-chip>
                  </div>
                  <div class="text-caption text-medium-emphasis mt-1">{{ strategy.description }}</div>
                </div>
                <v-text-field
                  v-if="sampleCountProperty(strategy)"
                  :model-value="strategySampleCount(strategy.ref.id)"
                  label="样本数"
                  type="number"
                  variant="outlined"
                  density="compact"
                  hide-details
                  class="audit-sample-input"
                  :min="sampleCountProperty(strategy)?.minimum"
                  :max="sampleCountProperty(strategy)?.maximum"
                  :step="sampleCountProperty(strategy)?.multipleOf || 1"
                  :disabled="!strategyState(strategy.ref.id).enabled"
                  @update:model-value="setStrategySampleCount(strategy.ref.id, $event)"
                />
              </div>
            </div>

            <div v-else>
              <v-select
                v-model="form.capabilityPresetKey"
                label="能力预设"
                :items="capabilityPresetOptions"
                variant="outlined"
                density="comfortable"
                :disabled="capabilityPresets.length === 0"
              />
              <div v-if="selectedCapabilityPreset" class="audit-preset-summary">
                <span>{{ selectedCapabilityPreset.tasks.length }} 个任务配置</span>
                <span>最多 {{ selectedCapabilityPreset.limits.requests }} 个请求</span>
                <span>{{ selectedCapabilityPreset.formalEligible ? '可生成正式结果' : '仅预览结果' }}</span>
              </div>
            </div>
          </section>

          <v-divider class="my-5" />

          <section class="audit-form-section">
            <div class="audit-section-heading">目标渠道</div>
            <v-autocomplete
              v-model="form.channelKeys"
              label="选择一个或多个渠道"
              :items="channelSelectItems"
              multiple
              chips
              closable-chips
              clearable
              variant="outlined"
              density="comfortable"
              :loading="channelLoading"
              :disabled="channelLoading"
            />

            <div v-if="selectedChannelOptions.length" class="audit-target-editor">
              <div v-for="option in selectedChannelOptions" :key="option.key" class="audit-target-row">
                <div class="audit-target-identity">
                  <v-chip size="x-small" variant="outlined">{{ protocolLabel(option.kind) }}</v-chip>
                  <span class="text-body-2 font-weight-medium">{{ option.channel.name }}</span>
                  <span class="text-caption text-medium-emphasis">{{ option.channel.id }}</span>
                </div>
                <v-text-field
                  v-model="targetSetting(option.key).model"
                  label="模型（留空使用渠道默认）"
                  variant="outlined"
                  density="compact"
                  hide-details
                />
                <v-select
                  v-model="targetSetting(option.key).thinking"
                  label="思考档位"
                  :items="thinkingOptions(option.kind)"
                  variant="outlined"
                  density="compact"
                  hide-details
                />
              </div>
            </div>
          </section>

          <v-divider class="my-5" />

          <section class="audit-form-section">
            <div class="audit-section-heading">低频调度</div>
            <div class="audit-form-grid audit-form-grid--three">
              <v-text-field
                v-model="form.startAtLocal"
                label="开始时间"
                type="datetime-local"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-model.number="form.intervalHours"
                label="执行间隔（小时）"
                type="number"
                min="6"
                max="720"
                step="1"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-model.number="form.jitterMinutes"
                label="随机抖动（分钟）"
                type="number"
                min="0"
                step="1"
                variant="outlined"
                density="comfortable"
              />
            </div>

            <div class="audit-form-grid audit-form-grid--three">
              <v-select
                v-model="form.endMode"
                label="结束方式"
                :items="endModeOptions"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-if="form.endMode === 'endAt'"
                v-model="form.endAtLocal"
                label="结束时间"
                type="datetime-local"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-else-if="form.endMode === 'duration'"
                v-model.number="form.durationHours"
                label="持续时长（小时）"
                type="number"
                min="1"
                step="1"
                variant="outlined"
                density="comfortable"
              />
              <v-text-field
                v-model="form.timeZone"
                label="时区"
                variant="outlined"
                density="comfortable"
              />
            </div>
          </section>

          <v-divider class="my-5" />

          <section class="audit-form-section">
            <div class="audit-section-heading">单次运行预算</div>
            <div class="audit-form-grid audit-form-grid--five">
              <v-text-field v-model.number="form.maxRequests" label="请求数" type="number" min="1" variant="outlined" density="comfortable" />
              <v-text-field v-model.number="form.maxInputTokens" label="输入 tokens" type="number" min="1" variant="outlined" density="comfortable" />
              <v-text-field v-model.number="form.maxOutputTokens" label="输出 tokens" type="number" min="1" variant="outlined" density="comfortable" />
              <v-text-field v-model.number="form.maxTotalTokens" label="总 tokens" type="number" min="1" variant="outlined" density="comfortable" />
              <v-text-field v-model.number="form.maxConcurrentRequests" label="并发" type="number" min="1" variant="outlined" density="comfortable" />
            </div>
          </section>
        </v-card-text>

        <v-divider />
        <v-card-actions class="audit-dialog-actions">
          <v-btn variant="text" :disabled="saving" @click="closeFormDialog">取消</v-btn>
          <v-btn color="primary" :loading="saving" @click="saveJob">{{ editingJob ? '保存修改' : '创建任务' }}</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="deleteDialog" max-width="460" persistent>
      <v-card>
        <v-card-title>删除审计任务</v-card-title>
        <v-card-text>
          确定删除“{{ deletingJob?.name }}”吗？历史运行和报告仍会保留。
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" :disabled="actionBusy" @click="deleteDialog = false">取消</v-btn>
          <v-btn color="error" :loading="actionKey === `${deletingJob?.id}:delete`" @click="deleteJob">删除</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="runsDialog" max-width="820" scrollable>
      <v-card>
        <v-card-title class="audit-dialog-title">
          <span>{{ runsJob?.name }} · 运行记录</span>
          <v-btn icon="mdi-close" variant="text" class="audit-icon-btn" aria-label="关闭" @click="runsDialog = false" />
        </v-card-title>
        <v-divider />
        <v-card-text class="pa-0">
          <v-progress-linear v-if="runsLoading" indeterminate />
          <v-alert v-if="runsError" type="error" variant="tonal" class="ma-4">{{ runsError }}</v-alert>
          <div v-else-if="!runsLoading && runs.length === 0" class="pa-8 text-center text-medium-emphasis">暂无运行记录</div>
          <div v-else class="audit-runs-list">
            <div v-for="run in runs" :key="run.id" class="audit-run-row">
              <div>
                <div class="d-flex flex-wrap align-center ga-2">
                  <span class="font-weight-medium">{{ formatDateTime(run.createdAt) }}</span>
                  <v-chip size="x-small" :color="runStatusColor(run.status)" variant="tonal">{{ runStatusLabel(run.status) }}</v-chip>
                  <v-chip size="x-small" variant="outlined">{{ run.trigger === 'manual' ? '手动' : '定时' }}</v-chip>
                </div>
                <div class="text-caption text-medium-emphasis mt-1">{{ run.id }}</div>
              </div>
              <div class="text-body-2">{{ run.usage.requests }}/{{ run.budget.maxRequests }} 请求</div>
              <div class="text-body-2">{{ formatCompactNumber(run.usage.totalTokens) }} tokens</div>
              <v-btn
                v-if="run.status === 'pending' || run.status === 'running'"
                color="error"
                variant="text"
                :loading="actionKey === `${run.id}:cancel`"
                :disabled="actionBusy"
                @click="cancelRun(run)"
              >
                取消
              </v-btn>
            </div>
          </div>
          <div v-if="runPageCount > 1" class="audit-runs-pagination">
            <v-pagination
              v-model="runPage"
              :length="runPageCount"
              :disabled="runsLoading || actionBusy"
              @update:model-value="loadCurrentRunPage"
            />
          </div>
        </v-card-text>
      </v-card>
    </v-dialog>

    <v-snackbar v-model="snackbar" :color="snackbarColor" location="top right" :timeout="3500">
      {{ snackbarText }}
    </v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import {
  ApiError,
  api,
  type ApiTab,
  type Channel,
  type ModelAuditCapabilityPreset,
  type ModelAuditJob,
  type ModelAuditJobDefinition,
  type ModelAuditJobStatus,
  type ModelAuditProtocolDescriptor,
  type ModelAuditRunPresentation,
  type ModelAuditStrategyDescriptor,
  type ModelAuditStrategySelection,
} from '@/services/api'

type EndMode = 'none' | 'endAt' | 'duration'

interface ChannelOption {
  key: string
  kind: ApiTab
  channel: Channel
  disabled: boolean
  disabledReason?: string
}

interface TargetSetting {
  model: string
  thinking: string
}

interface StrategyState {
  enabled: boolean
  config: Record<string, unknown>
}

interface AuditFormState {
  name: string
  initialStatus: 'draft' | 'enabled'
  workloadKind: 'identity' | 'capability'
  capabilityPresetKey: string
  strategyStates: Record<string, StrategyState>
  channelKeys: string[]
  targetSettings: Record<string, TargetSetting>
  startAtLocal: string
  endMode: EndMode
  endAtLocal: string
  durationHours: number
  intervalHours: number
  jitterMinutes: number
  timeZone: string
  maxRequests: number
  maxInputTokens: number
  maxOutputTokens: number
  maxTotalTokens: number
  maxConcurrentRequests: number
}

const pageSize = 20
const page = ref(1)
const totalJobs = ref(0)
const jobs = ref<ModelAuditJob[]>([])
const loading = ref(false)
const pageError = ref('')

const strategies = ref<ModelAuditStrategyDescriptor[]>([])
const capabilityPresets = ref<ModelAuditCapabilityPreset[]>([])
const protocolDescriptors = ref<ModelAuditProtocolDescriptor[]>([])
const channelOptions = ref<ChannelOption[]>([])
const catalogLoading = ref(false)
const channelLoading = ref(false)
const catalogNotice = ref('')

const formDialog = ref(false)
const editingJob = ref<ModelAuditJob | null>(null)
const saving = ref(false)
const formError = ref('')

const deleteDialog = ref(false)
const deletingJob = ref<ModelAuditJob | null>(null)
const actionKey = ref('')

const runsDialog = ref(false)
const runsJob = ref<ModelAuditJob | null>(null)
const runs = ref<ModelAuditRunPresentation[]>([])
const runsLoading = ref(false)
const runsError = ref('')
const runPageSize = 20
const runPage = ref(1)
const totalRuns = ref(0)

const snackbar = ref(false)
const snackbarText = ref('')
const snackbarColor = ref<'success' | 'error' | 'info'>('success')

const initialStatusOptions = [
  { title: '草稿', value: 'draft' },
  { title: '创建后启用', value: 'enabled' },
]

const endModeOptions = [
  { title: '持续运行', value: 'none' },
  { title: '指定结束时间', value: 'endAt' },
  { title: '指定持续时长', value: 'duration' },
]

const protocolNames: Record<ApiTab, string> = {
  messages: 'Messages',
  responses: 'Responses',
  gemini: 'Gemini',
  chat: 'Chat',
  images: 'Images',
}

const pageCount = computed(() => Math.max(1, Math.ceil(totalJobs.value / pageSize)))
const runPageCount = computed(() => Math.max(1, Math.ceil(totalRuns.value / runPageSize)))
const actionBusy = computed(() => Boolean(actionKey.value))
const catalogReady = computed(() => strategies.value.length > 0 && capabilityPresets.value.length > 0 && protocolDescriptors.value.length > 0)
const selectedChannelOptions = computed(() => {
  const keys = new Set(form.channelKeys)
  return channelOptions.value.filter(option => keys.has(option.key) && !option.disabled)
})
const channelSelectItems = computed(() => channelOptions.value.map(option => ({
  title: option.disabledReason ? `${protocolLabel(option.kind)} · ${option.channel.name} · ${option.disabledReason}` : `${protocolLabel(option.kind)} · ${option.channel.name}`,
  value: option.key,
  props: { disabled: option.disabled },
})))
const capabilityPresetOptions = computed(() => capabilityPresets.value.map(preset => ({
  title: `${capabilityModeLabel(preset.mode)} · v${preset.ref.semanticVersion} · ${preset.tasks.length} 项配置`,
  value: versionedRefKey(preset.ref),
})))
const selectedCapabilityPreset = computed(() => capabilityPresets.value.find(item => versionedRefKey(item.ref) === form.capabilityPresetKey))

const form = reactive<AuditFormState>(newFormState())

function newFormState(): AuditFormState {
  const start = new Date(Date.now() + 5 * 60 * 1000)
  return {
    name: '', initialStatus: 'draft', workloadKind: 'identity', capabilityPresetKey: '', strategyStates: {},
    channelKeys: [], targetSettings: {}, startAtLocal: toLocalDateTime(start), endMode: 'none', endAtLocal: '',
    durationHours: 168, intervalHours: 24, jitterMinutes: 0,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    maxRequests: 100, maxInputTokens: 100_000, maxOutputTokens: 100_000, maxTotalTokens: 200_000,
    maxConcurrentRequests: 1,
  }
}

function resetForm(state: AuditFormState) {
  Object.assign(form, state)
}

async function loadJobs() {
  loading.value = true
  pageError.value = ''
  try {
    const response = await api.listModelAuditJobs(page.value, pageSize)
    jobs.value = response.page.jobs
    totalJobs.value = response.page.total
    if (page.value > pageCount.value) {
      page.value = pageCount.value
      await loadJobs()
    }
  } catch (error) {
    pageError.value = errorMessage(error, '加载审计任务失败')
  } finally {
    loading.value = false
  }
}

async function loadCatalogs() {
  catalogLoading.value = true
  catalogNotice.value = ''
  try {
    const [strategyResponse, capabilityResponse, capabilityDescriptors] = await Promise.all([
      api.getModelAuditStrategies(),
      api.getModelAuditCapabilityPresets(),
      api.getModelAuditCapabilities(),
    ])
    if (!strategyResponse.available) throw new Error(strategyResponse.reason || '身份策略目录不可用')
    if (!capabilityResponse.catalog.available) throw new Error(capabilityResponse.catalog.reason || '能力预设目录不可用')
    strategies.value = strategyResponse.strategies
    capabilityPresets.value = capabilityResponse.catalog.presets
    protocolDescriptors.value = capabilityDescriptors.protocols
  } catch (error) {
    catalogNotice.value = errorMessage(error, '加载审计目录失败')
  } finally {
    catalogLoading.value = false
  }
}

async function loadChannels() {
  channelLoading.value = true
  const kinds: ApiTab[] = ['messages', 'responses', 'gemini', 'chat', 'images']
  const settled = await Promise.allSettled(kinds.map(kind => api.getChannelDashboard(kind)))
  const loaded: ChannelOption[] = []
  const failed: string[] = []
  settled.forEach((result, index) => {
    const kind = kinds[index]
    if (result.status === 'rejected') {
      failed.push(protocolLabel(kind))
      return
    }
    result.value.channels.forEach(channel => {
      const hasStableId = Boolean(channel.id?.trim())
      const unsupported = kind === 'images'
      loaded.push({
        key: `${kind}:${channel.id || channel.index}`,
        kind,
        channel,
        disabled: !hasStableId || unsupported,
        disabledReason: unsupported ? '审计资产不支持图片协议' : (!hasStableId ? '缺少稳定 ID' : undefined),
      })
    })
  })
  channelOptions.value = loaded
  channelLoading.value = false
  if (failed.length > 0) {
    catalogNotice.value = `以下渠道目录加载失败：${failed.join('、')}`
  }
}

function openCreateDialog() {
  const state = newFormState()
  const defaultPreset = capabilityPresets.value.find(item => item.mode === 'quick') || capabilityPresets.value[0]
  state.capabilityPresetKey = defaultPreset ? versionedRefKey(defaultPreset.ref) : ''
  for (const strategy of strategies.value) {
    state.strategyStates[strategy.ref.id] = { enabled: true, config: defaultStrategyConfig(strategy) }
  }
  editingJob.value = null
  resetForm(state)
  formError.value = ''
  formDialog.value = true
}

function openEditDialog(job: ModelAuditJob) {
  const state = newFormState()
  state.name = job.name
  state.workloadKind = job.workload.kind
  const existingStrategyIds = new Set((job.workload.strategies || []).map(item => item.strategyId))
  const availableStrategyIds = new Set(strategies.value.map(strategy => strategy.ref.id))
  const unavailableStrategies = [...existingStrategyIds].filter(id => !availableStrategyIds.has(id))
  if (job.workload.kind === 'identity' && unavailableStrategies.length > 0) {
    showSnackbar(`以下历史策略当前不可用，已阻止编辑：${unavailableStrategies.join('、')}`, 'error')
    return
  }
  if (job.workload.kind === 'capability' && job.workload.capabilityPreset) {
    state.capabilityPresetKey = versionedRefKey(job.workload.capabilityPreset)
    if (!capabilityPresets.value.some(item => versionedRefKey(item.ref) === state.capabilityPresetKey)) {
      showSnackbar(`历史能力预设当前不可用，已阻止编辑：${job.workload.capabilityPreset.id}`, 'error')
      return
    }
  } else {
    const defaultPreset = capabilityPresets.value[0]
    state.capabilityPresetKey = defaultPreset ? versionedRefKey(defaultPreset.ref) : ''
  }
  state.startAtLocal = toLocalDateTime(new Date(job.schedule.startAt))
  state.intervalHours = job.schedule.intervalMs / 3_600_000
  state.jitterMinutes = job.schedule.jitterMs / 60_000
  state.timeZone = job.schedule.timeZone
  state.maxRequests = job.budget.maxRequests
  state.maxInputTokens = job.budget.maxInputTokens
  state.maxOutputTokens = job.budget.maxOutputTokens
  state.maxTotalTokens = job.budget.maxTotalTokens
  state.maxConcurrentRequests = job.budget.maxConcurrentRequests
  if (job.schedule.endAt) {
    state.endMode = 'endAt'
    state.endAtLocal = toLocalDateTime(new Date(job.schedule.endAt))
  } else if (job.schedule.durationMs) {
    state.endMode = 'duration'
    state.durationHours = job.schedule.durationMs / 3_600_000
  }
  const existingStrategies = new Map((job.workload.strategies || []).map(item => [item.strategyId, item]))
  for (const strategy of strategies.value) {
    const existing = existingStrategies.get(strategy.ref.id)
    state.strategyStates[strategy.ref.id] = {
      enabled: existing?.enabled || false,
      config: existing ? { ...existing.config } : defaultStrategyConfig(strategy),
    }
  }
  const optionsByIdentity = new Map(channelOptions.value.map(option => [`${option.kind}:${option.channel.id}`, option]))
  const unresolvedTargets: string[] = []
  for (const target of job.targets) {
    const option = optionsByIdentity.get(`${target.channelKind}:${target.channelId}`)
    if (!option || option.disabled) {
      unresolvedTargets.push(`${protocolLabel(target.channelKind)}:${target.channelId}`)
      continue
    }
    state.channelKeys.push(option.key)
    state.targetSettings[option.key] = { model: target.model || '', thinking: target.thinking || '' }
  }
  if (unresolvedTargets.length > 0) {
    showSnackbar(`以下历史目标当前无法解析，已阻止编辑：${unresolvedTargets.join('、')}`, 'error')
    return
  }
  editingJob.value = job
  resetForm(state)
  formError.value = ''
  formDialog.value = true
}

function closeFormDialog() {
  if (saving.value) return
  formDialog.value = false
  editingJob.value = null
  formError.value = ''
}

async function saveJob() {
  formError.value = ''
  const validationError = validateForm()
  if (validationError) {
    formError.value = validationError
    return
  }
  saving.value = true
  try {
    const definition = buildDefinition()
    if (editingJob.value) {
      await api.updateModelAuditJob(editingJob.value.id, editingJob.value.revision, definition)
      showSnackbar('任务已更新', 'success')
    } else {
      await api.createModelAuditJob(definition, form.initialStatus)
      showSnackbar('任务已创建', 'success')
    }
    formDialog.value = false
    editingJob.value = null
    await loadJobs()
  } catch (error) {
    formError.value = errorMessage(error, '保存审计任务失败')
    if (isConflict(error)) {
      await loadJobs()
      formDialog.value = false
      editingJob.value = null
      showSnackbar('任务已被其他操作修改，列表已刷新，请重新打开编辑', 'error')
    }
  } finally {
    saving.value = false
  }
}

function validateForm(): string {
  if (!form.name.trim()) return '请输入任务名称'
  if (selectedChannelOptions.value.length === 0) return '请至少选择一个具有稳定 ID 的文本渠道'
  if (form.workloadKind === 'identity') {
    const enabled = strategies.value.filter(strategy => strategyState(strategy.ref.id).enabled)
    if (enabled.length === 0) return '身份审计至少启用一个策略'
    for (const strategy of enabled) {
      const property = sampleCountProperty(strategy)
      if (!property) continue
      const value = strategySampleCount(strategy.ref.id)
      const step = property.multipleOf || 1
      if (!Number.isInteger(value) || value < (property.minimum || 1) || value > (property.maximum || strategy.maximumSamples) || value % step !== 0) {
        return `${strategyName(strategy.ref.id)} 的样本数无效`
      }
    }
  } else if (!selectedCapabilityPreset.value) {
    return '请选择能力预设'
  }
  for (const option of selectedChannelOptions.value) {
    const setting = targetSetting(option.key)
    if (!thinkingOptions(option.kind).some(item => item.value === setting.thinking)) {
      return `${protocolLabel(option.kind)} 不支持思考档位 ${setting.thinking || '默认'}`
    }
    if (!requestProfile(option.kind)) return `${protocolLabel(option.kind)} 缺少可用请求轮廓`
  }
  const start = new Date(form.startAtLocal)
  if (Number.isNaN(start.getTime())) return '开始时间无效'
  if (!Number.isFinite(form.intervalHours) || form.intervalHours < 6 || form.intervalHours > 720) return '执行间隔必须在 6 至 720 小时之间'
  if (!Number.isFinite(form.jitterMinutes) || form.jitterMinutes < 0 || form.jitterMinutes >= form.intervalHours * 60) return '随机抖动必须非负且小于执行间隔'
  if (!form.timeZone.trim()) return '请输入 IANA 时区'
  if (form.endMode === 'endAt') {
    const end = new Date(form.endAtLocal)
    if (Number.isNaN(end.getTime()) || end <= start) return '结束时间必须晚于开始时间'
  }
  if (form.endMode === 'duration' && (!Number.isFinite(form.durationHours) || form.durationHours <= 0)) return '持续时长必须大于 0'
  const budgets = [form.maxRequests, form.maxInputTokens, form.maxOutputTokens, form.maxTotalTokens, form.maxConcurrentRequests]
  if (budgets.some(value => !Number.isInteger(value) || value <= 0)) return '预算值必须是正整数'
  if (form.maxConcurrentRequests > form.maxRequests) return '并发上限不能超过请求数'
  return ''
}

function buildDefinition(): ModelAuditJobDefinition {
  const existingTargets = new Map((editingJob.value?.targets || []).map(target => [`${target.channelKind}:${target.channelId}`, target]))
  const targets = selectedChannelOptions.value.map(option => {
    const setting = targetSetting(option.key)
    const existing = existingTargets.get(`${option.kind}:${option.channel.id}`)
    return {
      id: existing?.id || `target:${option.kind}:${option.channel.id}`,
      channelId: option.channel.id!, channelKind: option.kind, protocol: option.kind,
      ...(setting.model.trim() ? { model: setting.model.trim() } : {}),
      ...(setting.thinking ? { thinking: setting.thinking } : {}),
      requestProfile: requestProfile(option.kind),
    }
  })
  let workload: ModelAuditJobDefinition['workload']
  if (form.workloadKind === 'identity') {
    const selections: ModelAuditStrategySelection[] = strategies.value.map(strategy => ({
      strategyId: strategy.ref.id,
      enabled: strategyState(strategy.ref.id).enabled,
      config: { ...strategyState(strategy.ref.id).config },
    }))
    const strategySetVersion = editingJob.value?.workload.kind === 'identity'
      ? editingJob.value.workload.strategySetVersion || '1.0.0'
      : '1.0.0'
    workload = { kind: 'identity', strategySetVersion, strategies: selections }
  } else {
    workload = { kind: 'capability', capabilityPreset: { ...selectedCapabilityPreset.value!.ref } }
  }
  const startAt = new Date(form.startAtLocal)
  const schedule: ModelAuditJobDefinition['schedule'] = {
    startAt: startAt.toISOString(), intervalMs: Math.round(form.intervalHours * 3_600_000),
    timeZone: form.timeZone.trim(), jitterMs: Math.round(form.jitterMinutes * 60_000),
  }
  if (form.endMode === 'endAt') schedule.endAt = new Date(form.endAtLocal).toISOString()
  if (form.endMode === 'duration') schedule.durationMs = Math.round(form.durationHours * 3_600_000)
  return {
    name: form.name.trim(), workload, targets, schedule,
    budget: {
      maxRequests: form.maxRequests, maxInputTokens: form.maxInputTokens, maxOutputTokens: form.maxOutputTokens,
      maxTotalTokens: form.maxTotalTokens, maxConcurrentRequests: form.maxConcurrentRequests,
    },
  }
}

async function startRun(job: ModelAuditJob) {
  actionKey.value = `${job.id}:run`
  try {
    const response = await api.startModelAuditJobRun(job.id, job.revision)
    showSnackbar(`运行已启动：${response.run.id}`, 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '启动审计运行失败'), 'error')
    if (isConflict(error)) await loadJobs()
  } finally {
    actionKey.value = ''
  }
}

async function toggleJob(job: ModelAuditJob) {
  const next: ModelAuditJobStatus = job.status === 'enabled' ? 'paused' : 'enabled'
  actionKey.value = `${job.id}:transition`
  try {
    await api.transitionModelAuditJob(job.id, job.revision, next)
    showSnackbar(next === 'enabled' ? '任务已启用' : '任务已暂停', 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '更新任务状态失败'), 'error')
    if (isConflict(error)) await loadJobs()
  } finally {
    actionKey.value = ''
  }
}

function confirmDelete(job: ModelAuditJob) {
  deletingJob.value = job
  deleteDialog.value = true
}

async function deleteJob() {
  const job = deletingJob.value
  if (!job) return
  actionKey.value = `${job.id}:delete`
  try {
    await api.deleteModelAuditJob(job.id, job.revision)
    deleteDialog.value = false
    deletingJob.value = null
    showSnackbar('任务已删除，历史记录保留', 'success')
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '删除审计任务失败'), 'error')
    if (isConflict(error)) {
      await loadJobs()
      deleteDialog.value = false
      deletingJob.value = null
      showSnackbar('任务已被其他操作修改，列表已刷新，请重新确认删除', 'error')
    }
  } finally {
    actionKey.value = ''
  }
}

async function openRunsDialog(job: ModelAuditJob) {
  runsJob.value = job
  runPage.value = 1
  totalRuns.value = 0
  runsDialog.value = true
  await loadRuns(job)
}

async function loadRuns(job: ModelAuditJob) {
  runsLoading.value = true
  runsError.value = ''
  try {
    const response = await api.listModelAuditRuns(job.id, runPage.value, runPageSize)
    runs.value = response.page.runs
    totalRuns.value = response.page.total
    if (runPage.value > runPageCount.value) {
      runPage.value = runPageCount.value
      await loadRuns(job)
    }
  } catch (error) {
    runsError.value = errorMessage(error, '加载运行记录失败')
  } finally {
    runsLoading.value = false
  }
}

async function loadCurrentRunPage() {
  if (runsJob.value) await loadRuns(runsJob.value)
}

async function cancelRun(run: ModelAuditRunPresentation) {
  actionKey.value = `${run.id}:cancel`
  try {
    await api.cancelModelAuditRun(run.id)
    showSnackbar('运行已取消', 'success')
    if (runsJob.value) await loadRuns(runsJob.value)
    await loadJobs()
  } catch (error) {
    showSnackbar(errorMessage(error, '取消运行失败'), 'error')
    if (isConflict(error) && runsJob.value) await loadRuns(runsJob.value)
  } finally {
    actionKey.value = ''
  }
}

function strategyState(id: string): StrategyState {
  if (!form.strategyStates[id]) form.strategyStates[id] = { enabled: false, config: {} }
  return form.strategyStates[id]
}

function setStrategyEnabled(id: string, enabled: boolean) {
  strategyState(id).enabled = enabled
}

function strategySampleCount(id: string): number {
  return Number(strategyState(id).config.sampleCount || 0)
}

function setStrategySampleCount(id: string, value: unknown) {
  strategyState(id).config.sampleCount = Number(value)
}

function sampleCountProperty(strategy: ModelAuditStrategyDescriptor) {
  return strategy.configSchema.properties?.sampleCount
}

function defaultStrategyConfig(strategy: ModelAuditStrategyDescriptor): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  for (const [name, property] of Object.entries(strategy.configSchema.properties || {})) {
    if (property.default !== undefined) config[name] = property.default
    else if (property.type === 'integer' || property.type === 'number') config[name] = property.minimum || 1
  }
  return config
}

function versionedRefKey(ref: { id: string; semanticVersion: string; implementationVersion: string }) {
  return JSON.stringify([ref.id, ref.semanticVersion, ref.implementationVersion])
}

function targetSetting(key: string): TargetSetting {
  if (!form.targetSettings[key]) {
    const option = channelOptions.value.find(item => item.key === key)
    const defaultThinking = option && thinkingOptions(option.kind).some(item => item.value === 'medium') ? 'medium' : ''
    form.targetSettings[key] = { model: '', thinking: defaultThinking }
  }
  return form.targetSettings[key]
}

function thinkingOptions(kind: ApiTab) {
  const descriptor = protocolDescriptors.value.find(item => item.protocol === kind)
  return [
    { title: '协议默认值', value: '' },
    ...(descriptor?.thinking.mappings || []).map(mapping => ({ title: thinkingLabel(mapping.level), value: mapping.level })),
  ]
}

function requestProfile(kind: ApiTab): string {
  const profiles = protocolDescriptors.value.find(item => item.protocol === kind)?.profiles || []
  return profiles.find(item => item.id.includes('standard'))?.id || profiles[0]?.id || ''
}

function canEdit(job: ModelAuditJob) {
  return catalogReady.value && !channelLoading.value && (job.status === 'draft' || job.status === 'enabled' || job.status === 'paused')
}

function canToggle(job: ModelAuditJob) {
  return job.status === 'draft' || job.status === 'enabled' || job.status === 'paused'
}

function statusMeta(status: ModelAuditJobStatus) {
  const values: Record<ModelAuditJobStatus, { label: string; color: string }> = {
    draft: { label: '草稿', color: 'default' }, enabled: { label: '已启用', color: 'success' },
    running: { label: '运行中', color: 'info' }, paused: { label: '已暂停', color: 'warning' },
    expired: { label: '已结束', color: 'default' }, deleted: { label: '已删除', color: 'error' },
  }
  return values[status]
}

function workloadLabel(job: ModelAuditJob) {
  return job.workload.kind === 'identity' ? '身份与混用' : '通用能力'
}

function workloadDetail(job: ModelAuditJob) {
  if (job.workload.kind === 'identity') return `${job.workload.strategies?.filter(item => item.enabled).length || 0} 个策略`
  const preset = capabilityPresets.value.find(item => item.ref.id === job.workload.capabilityPreset?.id)
  return preset ? capabilityModeLabel(preset.mode) : (job.workload.capabilityPreset?.id || '未解析预设')
}

function targetSummary(job: ModelAuditJob) {
  return [...new Set(job.targets.map(target => protocolLabel(target.protocol)))].join('、')
}

function protocolLabel(kind: ApiTab) {
  return protocolNames[kind] || kind
}

function strategyName(id: string) {
  const names: Record<string, string> = {
    'identity.metadata-consistency': '元数据一致性',
    'identity.sol.reasoning-profile': 'Reasoning 轮廓',
    'identity.sol.rewrite-check': '32/48 改写检查',
    'identity.sol.synthetic-coverage': '合成覆盖检查',
    'identity.sol.probability-profile': '概率行为分布',
    'identity.sol.mode-difference': '模式差分',
    'identity.sol.variation-stability': '变形稳定性',
  }
  return names[id] || id
}

function capabilityModeLabel(mode: ModelAuditCapabilityPreset['mode']) {
  return mode === 'quick' ? '快速' : mode === 'standard' ? '标准' : '深入'
}

function thinkingLabel(level: string) {
  const labels: Record<string, string> = {
    off: '关闭', minimal: '最小', low: '低', medium: '中', high: '高', xhigh: '超高', adaptive: '自适应',
  }
  return labels[level] || level
}

function formatInterval(milliseconds: number) {
  const hours = milliseconds / 3_600_000
  return hours % 24 === 0 ? `每 ${hours / 24} 天` : `每 ${hours} 小时`
}

function formatDateTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString('zh-CN', { hour12: false })
}

function formatCompactNumber(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

function toLocalDateTime(date: Date) {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function runStatusLabel(status: ModelAuditRunPresentation['status']) {
  const labels: Record<ModelAuditRunPresentation['status'], string> = {
    pending: '等待中', running: '运行中', completed: '已完成', partial: '部分完成',
    failed: '失败', cancelled: '已取消',
  }
  return labels[status]
}

function runStatusColor(status: ModelAuditRunPresentation['status']) {
  if (status === 'completed') return 'success'
  if (status === 'running' || status === 'pending') return 'info'
  if (status === 'partial') return 'warning'
  return 'error'
}

function showSnackbar(message: string, color: 'success' | 'error' | 'info') {
  snackbarText.value = message
  snackbarColor.value = color
  snackbar.value = true
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback
}

function isConflict(error: unknown) {
  return error instanceof ApiError && error.status === 409
}

onMounted(async () => {
  await Promise.all([loadCatalogs(), loadChannels(), loadJobs()])
})
</script>

<style scoped>
.audit-jobs-view {
  width: 100%;
  max-width: 1500px;
  margin: 0 auto;
}

.audit-page-header,
.audit-page-actions,
.audit-dialog-title,
.audit-dialog-actions {
  display: flex;
  align-items: center;
}

.audit-page-header {
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.audit-page-actions {
  gap: 8px;
}

.audit-list {
  width: 100%;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-list-header,
.audit-list-row {
  display: grid;
  grid-template-columns: minmax(240px, 1.6fr) minmax(150px, 1fr) minmax(130px, 0.8fr) minmax(155px, 1fr) minmax(115px, 0.7fr) minmax(264px, auto);
  gap: 16px;
  align-items: center;
}

.audit-list-header {
  min-height: 42px;
  padding: 0 12px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.75rem;
  font-weight: 600;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-list-row {
  min-height: 92px;
  padding: 12px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  transition: background-color 160ms ease;
}

.audit-list-row:hover {
  background: rgba(var(--v-theme-on-surface), 0.035);
}

.audit-job-title-line,
.audit-row-actions,
.audit-target-identity,
.audit-preset-summary {
  display: flex;
  align-items: center;
}

.audit-job-title-line {
  flex-wrap: wrap;
  gap: 8px;
}

.audit-job-name,
.audit-target-summary {
  overflow-wrap: anywhere;
}

.audit-row-actions {
  justify-content: flex-end;
  gap: 2px;
}

.audit-icon-btn {
  min-width: 44px;
  min-height: 44px;
}

.audit-empty-state {
  padding: 64px 16px;
  text-align: center;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-dialog-title {
  min-height: 60px;
  justify-content: space-between;
  gap: 16px;
  font-size: 1.1rem;
}

.audit-form-body {
  padding: 24px;
}

.audit-section-heading {
  margin-bottom: 14px;
  font-size: 0.875rem;
  font-weight: 700;
}

.audit-form-grid {
  display: grid;
  gap: 16px;
}

.audit-form-grid--two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.audit-form-grid--three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.audit-form-grid--five {
  grid-template-columns: repeat(5, minmax(130px, 1fr));
}

.audit-strategy-list,
.audit-target-editor {
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-strategy-row {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) 120px;
  gap: 12px;
  align-items: center;
  min-height: 76px;
  padding: 10px 0;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-sample-input {
  width: 120px;
}

.audit-preset-summary {
  flex-wrap: wrap;
  gap: 8px 20px;
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 0.8125rem;
}

.audit-target-row {
  display: grid;
  grid-template-columns: minmax(220px, 1.2fr) minmax(220px, 1fr) minmax(150px, 0.7fr);
  gap: 12px;
  align-items: center;
  min-height: 72px;
  padding: 10px 0;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-target-identity {
  min-width: 0;
  flex-wrap: wrap;
  gap: 8px;
}

.audit-target-identity .text-caption {
  max-width: 100%;
  overflow-wrap: anywhere;
}

.audit-dialog-actions {
  min-height: 64px;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 20px;
}

.audit-runs-list {
  width: 100%;
}

.audit-runs-pagination {
  display: flex;
  justify-content: center;
  padding: 16px;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.audit-run-row {
  display: grid;
  grid-template-columns: minmax(260px, 1.5fr) minmax(120px, 0.6fr) minmax(120px, 0.6fr) auto;
  gap: 16px;
  align-items: center;
  min-height: 76px;
  padding: 12px 20px;
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

@media (max-width: 1100px) {
  .audit-list-header,
  .audit-list-row {
    grid-template-columns: minmax(220px, 1.5fr) minmax(140px, 1fr) minmax(120px, 0.7fr) minmax(150px, 1fr) minmax(264px, auto);
  }

  .audit-list-header > :nth-child(5),
  .audit-list-row > :nth-child(5) {
    display: none;
  }

  .audit-form-grid--five {
    grid-template-columns: repeat(3, minmax(150px, 1fr));
  }
}

@media (max-width: 760px) {
  .audit-page-header {
    align-items: flex-start;
  }

  .audit-page-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .audit-list-header {
    display: none;
  }

  .audit-list-row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
    min-height: 0;
    padding: 16px 4px;
  }

  .audit-list-row > [data-label] {
    display: grid;
    grid-template-columns: 86px minmax(0, 1fr);
    gap: 10px;
    align-items: start;
  }

  .audit-list-row > [data-label]::before {
    content: attr(data-label);
    color: rgb(var(--v-theme-on-surface-variant));
    font-size: 0.75rem;
    font-weight: 600;
  }

  .audit-list-row > .audit-row-actions {
    position: relative;
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
    padding-left: 96px;
  }

  .audit-list-row > .audit-row-actions::before {
    position: absolute;
    top: 12px;
    left: 0;
  }

  .audit-job-main > * {
    grid-column: 2;
  }

  .audit-row-actions {
    justify-content: flex-start;
  }

  .audit-form-body {
    padding: 18px 16px;
  }

  .audit-form-grid--two,
  .audit-form-grid--three,
  .audit-form-grid--five,
  .audit-target-row,
  .audit-run-row {
    grid-template-columns: 1fr;
  }

  .audit-strategy-row {
    grid-template-columns: 44px minmax(0, 1fr);
  }

  .audit-sample-input {
    grid-column: 2;
    width: 100%;
  }

  .audit-run-row {
    gap: 8px;
  }
}
</style>
