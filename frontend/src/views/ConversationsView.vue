<template>
  <div class="conversation-view">
    <v-card class="mb-4 pa-4" rounded="lg">
      <div class="d-flex flex-wrap ga-3 align-center">
        <v-text-field
          v-model="searchText"
          label="搜索对话"
          variant="outlined"
          density="compact"
          prepend-inner-icon="mdi-magnify"
          hide-details
          style="max-width: 320px;"
          @keyup.enter="loadConversations()"
        />

        <v-select
          v-model="kindFilter"
          :items="kindItems"
          label="类型"
          variant="outlined"
          density="compact"
          hide-details
          style="max-width: 180px;"
        />

        <v-btn color="primary" prepend-icon="mdi-refresh" variant="tonal" :loading="loading" @click="loadConversations()">
          刷新
        </v-btn>

        <v-spacer />

        <div class="text-body-2 text-medium-emphasis">
          共 {{ conversations.length }} 条
        </div>
      </div>
    </v-card>

    <v-card rounded="lg">
      <v-data-table
        :headers="headers"
        :items="filteredConversations"
        :loading="loading"
        item-key="id"
        density="compact"
      >
        <template #item.id="{ item }">
          <span class="clickable-id font-mono text-primary font-weight-bold" title="点击查看对话详情" @click="openDetailDialog(item)">
            {{ item.id }}
          </span>
        </template>

        <template #item.apiKind="{ item }">
          <v-chip size="small" variant="tonal">{{ item.apiKind }}</v-chip>
        </template>

        <template #item.firstPrompt="{ item }">
          <div v-if="conversationPrompts(item).length > 0" class="conversation-prompts clickable-prompt" title="点击查看对话详情" @click="openDetailDialog(item)">
            <div
              v-for="(prompt, idx) in conversationPrompts(item)"
              :key="idx"
              class="conversation-prompt-line"
            >
              <span class="prompt-index">{{ idx + 1 }}</span>
              <span class="conversation-prompt-text">{{ prompt }}</span>
            </div>
          </div>
          <span v-else class="text-medium-emphasis">--</span>
        </template>

        <template #item.lastModel="{ item }">
          <div class="model-chain">
            <span class="model-name">{{ formatModelChain(item) }}</span>
          </div>
        </template>

        <template #item.activity="{ item }">
          <v-chip
            v-if="item.isSending"
            size="small"
            color="success"
            variant="tonal"
            prepend-icon="mdi-loading"
            class="sending-chip"
          >
            发送中 {{ formatDurationSince(item.lastRequestAt || item.lastSeenAt) }}
          </v-chip>
          <span v-else class="text-caption text-medium-emphasis">
            {{ formatIdleText(item) }}
          </span>
        </template>

        <template #item.routeOverride="{ item }">
          <div v-if="item.routeOverride" class="resolved-channel">
            <div>
              {{ item.routeOverride.kind }} / #{{ item.routeOverride.channelIndex }}
              <span v-if="item.routeOverride.channelName">({{ item.routeOverride.channelName }})</span>
            </div>
            <div v-if="formatRouteOverrideModel(item)" class="text-caption text-medium-emphasis">
              {{ formatRouteOverrideModel(item) }}
            </div>
          </div>
          <span v-else class="text-medium-emphasis">默认调度</span>
        </template>

        <template #item.lastResolved="{ item }">
          <div v-if="item.lastResolved" class="resolved-channel">
            <div>
              {{ item.lastResolved.kind }} / #{{ item.lastResolved.channelIndex }}
              <span v-if="item.lastResolved.channelName">({{ item.lastResolved.channelName }})</span>
            </div>
            <div class="text-caption text-medium-emphasis">
              {{ formatModelChain(item) }}
            </div>
          </div>
          <span v-else class="text-medium-emphasis">未解析</span>
        </template>

        <template #item.actions="{ item }">
          <v-btn size="small" variant="text" prepend-icon="mdi-swap-horizontal" @click="openRouteDialog(item)">
            固定渠道
          </v-btn>
          <v-btn
            v-if="item.routeOverride"
            size="small"
            variant="text"
            color="warning"
            prepend-icon="mdi-close"
            @click="clearRoute(item)"
          >
            清除
          </v-btn>
        </template>
      </v-data-table>
    </v-card>

    <v-dialog v-model="routeDialog" max-width="640">
      <v-card rounded="lg">
        <v-card-title>设置固定渠道</v-card-title>
        <v-card-text>
          <div class="mb-3 text-body-2 text-medium-emphasis">
            {{ editingConversation?.id }}
          </div>
          <div class="mb-3">
            <v-chip size="small" variant="tonal">{{ editingConversation?.apiKind }}</v-chip>
          </div>
          <v-select
            v-model="selectedChannelIndex"
            :items="routeChannelItems"
            label="渠道"
            variant="outlined"
            density="compact"
            class="mt-3"
          />
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="routeDialog = false">取消</v-btn>
          <v-btn color="primary" :loading="saving" @click="saveRoute">保存</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <!-- 详情 Dialog -->
    <v-dialog v-model="detailDialog" max-width="720">
      <v-card rounded="lg" class="pa-2">
        <v-card-title class="d-flex align-center justify-between border-b pb-2">
          <span class="text-h6 font-weight-bold">💬 对话可观测详情</span>
          <v-spacer />
          <v-btn icon="mdi-close" variant="text" size="small" @click="detailDialog = false" />
        </v-card-title>
        
        <v-card-text class="pt-4 pb-2">
          <!-- ID & Meta -->
          <div class="d-flex flex-wrap align-center justify-space-between mb-4 ga-2">
            <div class="d-flex align-center ga-2">
              <span class="text-subtitle-1 font-weight-bold font-mono">{{ selectedConversation?.id }}</span>
              <v-btn icon="mdi-content-copy" size="x-small" variant="text" color="primary" title="复制ID" @click="copyText(selectedConversation?.id || '')" />
            </div>
            <div class="d-flex align-center ga-2">
              <v-chip size="small" color="primary" variant="tonal" class="text-uppercase">{{ selectedConversation?.apiKind }}</v-chip>
              <v-chip v-if="selectedConversation?.isSending" size="small" color="success" variant="tonal" prepend-icon="mdi-loading" class="sending-chip">发送中</v-chip>
              <v-chip v-else size="small" color="grey" variant="tonal">已完成/空闲</v-chip>
            </div>
          </div>

          <!-- Grid Info -->
          <v-row class="mb-2" dense>
            <v-col cols="12" sm="6">
              <div class="info-card">
                <span class="info-label">请求模型</span>
                <span class="info-value font-mono">{{ selectedConversation?.lastModel || '--' }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="6">
              <div class="info-card">
                <span class="info-label">实际路由模型</span>
                <span class="info-value font-mono">{{ selectedConversation?.lastResolvedModel || '--' }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="6">
              <div class="info-card">
                <span class="info-label">首次看到时间</span>
                <span class="info-value text-caption">{{ selectedConversation?.firstSeenAt ? new Date(selectedConversation.firstSeenAt).toLocaleString() : '--' }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="6">
              <div class="info-card">
                <span class="info-label">最近活动时间</span>
                <span class="info-value text-caption">{{ selectedConversation?.lastSeenAt ? new Date(selectedConversation.lastSeenAt).toLocaleString() : '--' }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="4">
              <div class="info-card text-center">
                <span class="info-label">总请求次数</span>
                <span class="info-value text-h6 text-primary">{{ selectedConversation?.requestCount || 0 }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="4">
              <div class="info-card text-center">
                <span class="info-label">总错误次数</span>
                <span class="info-value text-h6 text-error">{{ selectedConversation?.errorCount || 0 }}</span>
              </div>
            </v-col>
            <v-col cols="12" sm="4">
              <div class="info-card text-center">
                <span class="info-label">固定渠道</span>
                <span class="info-value text-body-2">
                  <span v-if="selectedConversation?.routeOverride">
                    #{{ selectedConversation.routeOverride.channelIndex }}
                  </span>
                  <span v-else class="text-medium-emphasis">默认调度</span>
                </span>
              </div>
            </v-col>
          </v-row>

          <!-- 错误信息 (如果有) -->
          <v-alert
            v-if="selectedConversation?.lastError"
            type="error"
            variant="tonal"
            icon="mdi-alert-circle"
            title="最近一次请求报错"
            class="mb-4 text-caption border-error border"
            density="compact"
          >
            <div class="d-flex align-start justify-space-between mt-1">
              <pre class="error-pre">{{ selectedConversation.lastError }}</pre>
              <v-btn size="x-small" icon="mdi-content-copy" variant="text" color="error" title="复制报错" @click="copyText(selectedConversation.lastError || '')" />
            </div>
          </v-alert>

          <!-- 提示词详情 -->
          <div class="mt-4">
            <div class="text-subtitle-2 font-weight-bold mb-3 d-flex align-center ga-2">
              <span class="prompt-section-marker" aria-hidden="true"></span>
              <span>对话提示词历史</span>
            </div>
            <div class="text-caption text-medium-emphasis mb-2">
              最多保存该会话前 3 条不同的真实用户输入，以便快速识别会话内容
            </div>

            <!-- 无提示词 -->
            <div v-if="!selectedConversation || conversationPrompts(selectedConversation).length === 0" class="text-center py-4 border rounded text-medium-emphasis text-body-2 bg-grey-lighten-4">
              暂无提示词记录
            </div>

            <!-- 提示词卡片列表 -->
            <div v-else class="d-flex flex-column ga-3">
              <div
                v-for="(prompt, idx) in conversationPrompts(selectedConversation)"
                :key="idx"
                class="prompt-detail-card"
              >
                <div class="prompt-card-header">
                  <v-chip size="x-small" color="primary" variant="flat" class="font-weight-bold">
                    输入 #{{ idx + 1 }}
                  </v-chip>
                  <v-spacer />
                  <v-btn
                    size="x-small"
                    variant="text"
                    icon="mdi-content-copy"
                    color="primary"
                    title="复制提示词"
                    @click="copyText(prompt)"
                  />
                </div>
                <div class="prompt-card-body">
                  {{ prompt }}
                </div>
              </div>
            </div>
          </div>
        </v-card-text>

        <v-card-actions class="border-t mt-2 pt-2">
          <v-spacer />
          <v-btn color="primary" variant="tonal" prepend-icon="mdi-swap-horizontal" @click="openRouteOverrideFromDetail()">
            去固定该会话渠道
          </v-btn>
          <v-btn variant="outlined" @click="detailDialog = false">关闭</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-snackbar v-model="snackbar" :color="snackbarColor" timeout="2000">
      {{ snackbarText }}
    </v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type ConversationEntry, type ConversationKind, type ConversationRouteOptionChannel } from '@/services/api'

const headers = [
  { title: 'ID', key: 'id' },
  { title: '提示词', key: 'firstPrompt', sortable: false },
  { title: '类型', key: 'apiKind' },
  { title: '模型', key: 'lastModel' },
  { title: '状态', key: 'activity', sortable: false },
  { title: '请求数', key: 'requestCount' },
  { title: '错误数', key: 'errorCount' },
  { title: '固定渠道', key: 'routeOverride' },
  { title: '最近解析', key: 'lastResolved' },
  { title: '操作', key: 'actions', sortable: false }
]

const kindItems: ConversationKind[] = ['messages', 'responses', 'gemini', 'chat']
type RouteSelectItem = {
  title: string
  value: number
  channel: ConversationRouteOptionChannel
}

const loading = ref(false)
const saving = ref(false)
const conversations = ref<ConversationEntry[]>([])
const searchText = ref('')
const kindFilter = ref<ConversationKind | ''>('')
const now = ref(Date.now())
const routeDialog = ref(false)
const editingConversation = ref<ConversationEntry | null>(null)
const selectedChannelIndex = ref<number | null>(null)
const routeOptions = ref<Record<ConversationKind, RouteSelectItem[]>>({
  messages: [],
  responses: [],
  gemini: [],
  chat: []
})
let clockTimer: ReturnType<typeof setInterval> | null = null
let refreshTimer: ReturnType<typeof setInterval> | null = null

const detailDialog = ref(false)
const selectedConversation = ref<ConversationEntry | null>(null)
const snackbar = ref(false)
const snackbarText = ref('')
const snackbarColor = ref('success')

const openDetailDialog = (item: ConversationEntry) => {
  selectedConversation.value = item
  detailDialog.value = true
}

const openRouteOverrideFromDetail = () => {
  if (!selectedConversation.value) return
  detailDialog.value = false
  openRouteDialog(selectedConversation.value)
}

const copyText = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    snackbarText.value = '已复制到剪贴板'
    snackbarColor.value = 'success'
    snackbar.value = true
  } catch (e) {
    snackbarText.value = '复制失败，请手动选择复制'
    snackbarColor.value = 'error'
    snackbar.value = true
  }
}

const filteredConversations = computed(() => {
  const q = searchText.value.trim().toLowerCase()
  return conversations.value.filter(item => {
    if (kindFilter.value && item.apiKind !== kindFilter.value) return false
    if (!q) return true
    const promptMatch = item.prompts && item.prompts.some(p => p.toLowerCase().includes(q))
    return [item.id, item.firstPrompt, item.lastModel, item.lastResolvedModel, item.lastError, item.routeOverride?.channelName, item.lastResolved?.channelName]
      .filter(Boolean)
      .some(value => String(value).toLowerCase().includes(q)) || promptMatch
  })
})

const conversationPrompts = (item?: ConversationEntry | null): string[] => {
  if (!item) return []
  const prompts = Array.isArray(item.prompts) ? item.prompts : []
  const values = prompts.length > 0 ? prompts : [item.firstPrompt || '']
  const seen = new Set<string>()
  return values
    .map(value => String(value || '').trim())
    .filter(value => {
      if (!value || seen.has(value)) return false
      seen.add(value)
      return true
    })
    .slice(0, 3)
}

const routeChannelItems = computed(() => {
  const kind = editingConversation.value?.apiKind
  return kind ? routeOptions.value[kind] ?? [] : []
})

const loadRouteOptions = async () => {
  const res = await api.getConversationRouteOptions()
  const next: Record<ConversationKind, RouteSelectItem[]> = {
    messages: [],
    responses: [],
    gemini: [],
    chat: []
  }
  for (const group of res.kinds) {
    next[group.kind] = group.channels.map(ch => ({
      title: formatRouteOptionTitle(ch),
      value: ch.channelIndex,
      channel: ch
    }))
  }
  routeOptions.value = next
}

const loadConversations = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    const res = await api.getConversations({
      q: searchText.value || undefined,
      kind: kindFilter.value || undefined
    })
    conversations.value = res.conversations
  } finally {
    if (!silent) loading.value = false
  }
}

const openRouteDialog = (item: ConversationEntry) => {
  editingConversation.value = item
  selectedChannelIndex.value = item.routeOverride?.channelIndex ?? null
  routeDialog.value = true
}

const saveRoute = async () => {
  if (!editingConversation.value || selectedChannelIndex.value === null) return
  saving.value = true
  try {
    const updated = await api.setConversationRoute(
      editingConversation.value.id,
      editingConversation.value.apiKind,
      selectedChannelIndex.value
    )
    conversations.value = conversations.value.map(item =>
      item.id === updated.id ? updated : item
    )
    routeDialog.value = false
  } finally {
    saving.value = false
  }
}

const clearRoute = async (item: ConversationEntry) => {
  const updated = await api.clearConversationRoute(item.id)
  conversations.value = conversations.value.map(entry =>
    entry.id === updated.id ? updated : entry
  )
}

const formatModelChain = (item: ConversationEntry): string => {
  const requested = (item.lastModel || fallbackModelLabel(item.apiKind)).trim()
  const resolved = (item.lastResolvedModel || '').trim()
  if (resolved && requested && resolved !== requested) {
    return `${requested} -> ${resolved}`
  }
  return resolved || requested || '--'
}

const fallbackModelLabel = (kind: ConversationKind): string => {
  if (kind === 'messages') return 'claude'
  if (kind === 'responses' || kind === 'chat') return 'GPT'
  if (kind === 'gemini') return 'gemini'
  return '--'
}

const formatRouteOptionTitle = (channel: ConversationRouteOptionChannel): string => {
  const modelPreview = formatRouteChannelModelPreview(channel)
  return modelPreview
    ? `#${channel.channelIndex} ${channel.channelName} · ${modelPreview}`
    : `#${channel.channelIndex} ${channel.channelName}`
}

const formatRouteOverrideModel = (item: ConversationEntry): string => {
  const override = item.routeOverride
  if (!override) return ''
  const channel = routeOptions.value[override.kind]?.find(option => option.value === override.channelIndex)?.channel
  return channel ? formatRouteChannelModelPreview(channel) : fallbackModelLabel(override.kind)
}

const formatRouteChannelModelPreview = (channel: ConversationRouteOptionChannel): string => {
  const defaultModel = String(channel.defaultModel || '').trim()
  if (defaultModel) return `兜底 ${defaultModel}`

  const entries = Object.entries(channel.modelMapping || {})
    .map(([source, target]) => [source.trim(), target.trim()] as const)
    .filter(([source, target]) => source && target)
  if (entries.length === 0) {
    if (channel.kind === 'messages') return 'claude'
    if (channel.kind === 'responses' || channel.kind === 'chat') return 'GPT'
    if (channel.kind === 'gemini') return 'gemini'
    return ''
  }

  const preferred = pickPreferredMapping(entries, channel)
  if (!preferred) return ''
  const [source, target] = preferred
  return source === target ? target : `${source} -> ${target}`
}

const pickPreferredMapping = (
  entries: Array<readonly [string, string]>,
  channel: ConversationRouteOptionChannel
): readonly [string, string] | undefined => {
  const lowerService = String(channel.serviceType || '').toLowerCase()
  const preferredTerms = channel.kind === 'messages' || lowerService.includes('claude')
    ? ['opus', 'sonnet', 'claude']
    : channel.kind === 'responses' || lowerService.includes('response') || lowerService.includes('openai') || lowerService.includes('chat')
      ? ['gpt', 'codex']
      : ['gemini']

  for (const term of preferredTerms) {
    const match = entries.find(([source]) => source.toLowerCase().includes(term))
    if (match) return match
  }

  return [...entries].sort((a, b) => a[0].localeCompare(b[0]))[0]
}

const formatDurationSince = (value?: string): string => {
  if (!value) return ''
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return ''
  const seconds = Math.max(0, Math.floor((now.value - timestamp) / 1000))
  if (seconds < 60) return `${seconds}秒`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}小时`
  return `${Math.floor(hours / 24)}天`
}

const formatIdleText = (item: ConversationEntry): string => {
  const value = item.lastRequestAt || item.lastSeenAt
  const duration = formatDurationSince(value)
  return duration ? `${duration}未发送` : '--'
}

onMounted(async () => {
  await Promise.all([loadRouteOptions(), loadConversations()])
  clockTimer = setInterval(() => {
    now.value = Date.now()
  }, 1000)
  refreshTimer = setInterval(() => {
    void loadConversations(true)
  }, 3000)
})

onUnmounted(() => {
  if (clockTimer) {
    clearInterval(clockTimer)
    clockTimer = null
  }
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
})
</script>

<style scoped>
.conversation-prompts {
  max-width: 420px;
}

.conversation-prompt-line {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: center;
  gap: 6px;
  min-height: 20px;
}

.conversation-prompt-line + .conversation-prompt-line {
  margin-top: 2px;
}

.prompt-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 4px;
  background: rgba(var(--v-theme-primary), 0.12);
  color: rgb(var(--v-theme-primary));
  font-size: 11px;
  font-weight: 700;
  line-height: 1;
}

.conversation-prompt-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.clickable-id {
  cursor: pointer;
  text-decoration: underline dotted;
}
.clickable-id:hover {
  opacity: 0.8;
}

.clickable-prompt {
  cursor: pointer;
}
.clickable-prompt:hover {
  text-decoration: underline;
  color: rgb(var(--v-theme-primary));
}

.info-card {
  background: #f8f9fa;
  border: 1px solid #e9ecef;
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  height: 100%;
}

.info-label {
  font-size: 11px;
  color: #6c757d;
  font-weight: 500;
  text-transform: uppercase;
  margin-bottom: 2px;
}

.info-value {
  font-size: 13px;
  color: #212529;
  font-weight: 600;
  word-break: break-all;
}

.error-pre {
  white-space: pre-wrap;
  word-break: break-all;
  font-family: 'SF Mono', Monaco, 'Cascadia Code', monospace;
  font-size: 11px;
  max-height: 120px;
  overflow-y: auto;
  flex: 1;
}

.prompt-detail-card {
  border: 1px solid #dee2e6;
  border-radius: 8px;
  overflow: hidden;
  background: #fff;
}

.prompt-section-marker {
  display: inline-block;
  width: 4px;
  height: 16px;
  border-radius: 2px;
  background: rgb(var(--v-theme-primary));
  flex: 0 0 auto;
}

.prompt-card-header {
  background: #f8f9fa;
  border-bottom: 1px solid #dee2e6;
  padding: 6px 12px;
  display: flex;
  align-items: center;
}

.prompt-card-body {
  padding: 12px;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
  color: #333;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 150px;
  overflow-y: auto;
}

.model-chain {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-name {
  font-family: 'SF Mono', Monaco, 'Cascadia Code', monospace;
  font-size: 12px;
}

.resolved-channel {
  min-width: 180px;
}

.sending-chip :deep(.v-icon) {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
