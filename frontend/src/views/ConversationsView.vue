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
          @keyup.enter="loadConversations"
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

        <v-btn color="primary" prepend-icon="mdi-refresh" variant="tonal" :loading="loading" @click="loadConversations">
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

        <template #item.routeOverride="{ item }">
          <span v-if="item.routeOverride">
            {{ item.routeOverride.kind }} / #{{ item.routeOverride.channelIndex }}
            <span v-if="item.routeOverride.channelName">({{ item.routeOverride.channelName }})</span>
          </span>
          <span v-else class="text-medium-emphasis">默认调度</span>
        </template>

        <template #item.lastResolved="{ item }">
          <span v-if="item.lastResolved">
            {{ item.lastResolved.kind }} / #{{ item.lastResolved.channelIndex }}
            <span v-if="item.lastResolved.channelName">({{ item.lastResolved.channelName }})</span>
          </span>
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
import { computed, onMounted, ref } from 'vue'
import { api, type ConversationEntry, type ConversationKind } from '@/services/api'

const headers = [
  { title: 'ID', key: 'id' },
  { title: '类型', key: 'apiKind' },
  { title: '模型', key: 'lastModel' },
  { title: '请求数', key: 'requestCount' },
  { title: '错误数', key: 'errorCount' },
  { title: '固定渠道', key: 'routeOverride' },
  { title: '最近解析', key: 'lastResolved' },
  { title: '操作', key: 'actions', sortable: false }
]

const kindItems: ConversationKind[] = ['messages', 'responses', 'gemini', 'chat']

const loading = ref(false)
const saving = ref(false)
const conversations = ref<ConversationEntry[]>([])
const searchText = ref('')
const kindFilter = ref<ConversationKind | ''>('')
const routeDialog = ref(false)
const editingConversation = ref<ConversationEntry | null>(null)
const selectedChannelIndex = ref<number | null>(null)
const routeOptions = ref<Record<ConversationKind, Array<{ title: string; value: number }>>>({
  messages: [],
  responses: [],
  gemini: [],
  chat: []
})

const filteredConversations = computed(() => {
  const q = searchText.value.trim().toLowerCase()
  return conversations.value.filter(item => {
    if (kindFilter.value && item.apiKind !== kindFilter.value) return false
    if (!q) return true
    return [item.id, item.lastModel, item.lastError, item.routeOverride?.channelName, item.lastResolved?.channelName]
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
  const next: Record<ConversationKind, Array<{ title: string; value: number }>> = {
    messages: [],
    responses: [],
    gemini: [],
    chat: []
  }
  for (const group of res.kinds) {
    next[group.kind] = group.channels.map(ch => ({
      title: `#${ch.channelIndex} ${ch.channelName}`,
      value: ch.channelIndex
    }))
  }
  routeOptions.value = next
}

const loadConversations = async () => {
  loading.value = true
  try {
    const res = await api.getConversations({
      q: searchText.value || undefined,
      kind: kindFilter.value || undefined
    })
    conversations.value = res.conversations
  } finally {
    loading.value = false
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

onMounted(async () => {
  await Promise.all([loadRouteOptions(), loadConversations()])
})
</script>
