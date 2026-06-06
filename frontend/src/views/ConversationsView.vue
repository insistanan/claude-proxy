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
        <template #item.apiKind="{ item }">
          <v-chip size="small" variant="tonal">{{ item.apiKind }}</v-chip>
        </template>

        <template #item.firstPrompt="{ item }">
          <div v-if="item.firstPrompt" class="conversation-prompt" :title="item.firstPrompt">
            {{ item.firstPrompt }}
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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type ConversationEntry, type ConversationKind, type ConversationRouteOptionChannel } from '@/services/api'

const headers = [
  { title: 'ID', key: 'id' },
  { title: '首个提示词', key: 'firstPrompt', sortable: false },
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

const filteredConversations = computed(() => {
  const q = searchText.value.trim().toLowerCase()
  return conversations.value.filter(item => {
    if (kindFilter.value && item.apiKind !== kindFilter.value) return false
    if (!q) return true
    return [item.id, item.firstPrompt, item.lastModel, item.lastResolvedModel, item.lastError, item.routeOverride?.channelName, item.lastResolved?.channelName]
      .filter(Boolean)
      .some(value => String(value).toLowerCase().includes(q))
  })
})

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
.conversation-prompt {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
