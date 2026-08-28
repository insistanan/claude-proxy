<template>
  <div>
    <div class="d-flex flex-wrap align-center ga-2 mb-3">
      <v-chip
        v-for="kind in EVAL_PROTOCOL_KINDS"
        :key="kind"
        size="small"
        :color="protocols.includes(kind) ? 'primary' : undefined"
        :variant="protocols.includes(kind) ? 'flat' : 'outlined'"
        @click="toggleProtocol(kind)"
      >
        {{ evalProtocolLabel(kind) }}
      </v-chip>

      <v-text-field
        :model-value="search"
        density="compact"
        variant="outlined"
        prepend-inner-icon="mdi-magnify"
        label="搜索渠道"
        hide-details
        clearable
        style="max-width: 220px"
        @update:model-value="search = ($event || '').trim()"
      />

      <v-btn size="small" variant="text" @click="selectAllFiltered">全选筛选</v-btn>
      <v-btn size="small" variant="text" @click="selectActiveFiltered">只选活跃</v-btn>
      <v-btn size="small" variant="text" :disabled="!modelValue.length" @click="emitSelection([])">清空</v-btn>
    </div>

    <div class="text-caption text-medium-emphasis mb-3">
      <v-icon size="small" class="mr-1">mdi-help-circle-outline</v-icon>
      每个渠道可单独指定模型（优先于「统一模型」）；留空则用该渠道自身 defaultModel。模型框聚焦时自动探测上游可用模型。已选
      {{ modelValue.length }} 个渠道。
    </div>

    <div v-if="!groups.length" class="text-body-2 text-medium-emphasis py-4">
      没有渠道
    </div>

    <div v-else class="pool-grid">
      <section v-for="group in groups" :key="group.key" class="pool-card">
        <header class="pool-card-header">
          <div class="text-body-2 font-weight-medium text-truncate">{{ group.title }}</div>
          <v-btn size="x-small" variant="text" @click="toggleGroup(group)">
            {{ isGroupFullySelected(group) ? '取消' : '全选' }}
          </v-btn>
        </header>

        <div
          v-for="channel in group.channels"
          :key="channel.id"
          class="channel-row"
          :class="{ 'channel-row--selected': isSelected(channel.id) }"
          role="button"
          tabindex="0"
          @click="toggleChannel(channel.id)"
          @keydown.enter.prevent="toggleChannel(channel.id)"
          @keydown.space.prevent="toggleChannel(channel.id)"
        >
          <v-icon size="18" :color="isSelected(channel.id) ? 'primary' : undefined">
            {{ isSelected(channel.id) ? 'mdi-checkbox-marked' : 'mdi-checkbox-blank-outline' }}
          </v-icon>
          <span class="channel-name text-truncate">{{ channel.name }}</span>
          <v-chip v-if="channel.status === 'suspended'" size="x-small" variant="tonal" color="grey">熔断</v-chip>
          <v-combobox
            :model-value="channelModelOf(channel.id)"
            :items="modelOptionsOf(channel.id)"
            :loading="modelLoadingOf(channel.id)"
            density="compact"
            variant="outlined"
            hide-details
            label="模型"
            placeholder="留空用渠道默认"
            class="channel-model"
            @click.stop
            @keydown.stop
            @focus="loadModels(channel.id)"
            @update:model-value="(value: unknown) => updateChannelModel(channel.id, value)"
          >
            <template #no-data>
              <div class="text-center px-2 text-caption text-medium-emphasis">
                <template v-if="modelLoadingOf(channel.id)">正在探测上游模型...</template>
                <template v-else>无匹配，可直接输入模型名</template>
              </div>
            </template>
          </v-combobox>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type ApiTab, type Channel, type ChannelPool } from '@/services/api'
import { EVAL_PROTOCOL_KINDS, evalProtocolLabel } from '@/utils/eval'

interface ChannelGroup {
  key: string
  title: string
  channels: Array<Channel & { id: string }>
}

const props = defineProps<{
  /** 四协议渠道，由 /eval 页统一拉取 */
  channelsByKind: Record<ApiTab, Channel[]>
  /** 各协议的渠道池，用来给分组卡片起名 */
  poolsByKind: Record<ApiTab, ChannelPool[]>
  /** 已勾选的渠道稳定 UUID */
  modelValue: string[]
  /** 当前生效的协议过滤 */
  protocols: ApiTab[]
  /** channelId → 渠道专属模型覆盖，优先于统一 model */
  channelModels?: Record<string, string>
}>()

const emit = defineEmits<{
  'update:modelValue': [string[]]
  'update:protocols': [ApiTab[]]
  'update:channelModels': [Record<string, string>]
}>()

const search = ref('')
const modelOptions = ref<Record<string, string[]>>({})
const modelLoading = ref<Record<string, boolean>>({})

const emitSelection = (ids: string[]) => emit('update:modelValue', ids)

const toggleProtocol = (kind: ApiTab) => {
  const next = props.protocols.includes(kind)
    ? props.protocols.filter(item => item !== kind)
    : [...props.protocols, kind]
  emit('update:protocols', next)
}

/** 备用池 / 弃用池不画：它们在评测里只会记 inapplicable。 */
const isSelectable = (channel: Channel): channel is Channel & { id: string } => {
  if (!channel.id) return false
  return channel.status !== 'disabled' && channel.status !== 'deprecated' && channel.status !== 'deleted'
}

const matchesSearch = (channel: Channel) => {
  if (!search.value) return true
  const keyword = search.value.toLowerCase()
  return (
    channel.name.toLowerCase().includes(keyword) ||
    (channel.defaultModel || '').toLowerCase().includes(keyword)
  )
}

const poolNameOf = (kind: ApiTab, poolId?: string) => {
  if (!poolId) return '未分组'
  return (props.poolsByKind[kind] || []).find(pool => pool.id === poolId)?.name || '未分组'
}

/**
 * 按「协议 × 渠道池」分组。不同协议的同名池不合并——同名不代表同一批上游，
 * 合并会让用户以为勾了一个其实勾了两个协议的渠道。
 */
const groups = computed<ChannelGroup[]>(() => {
  const map = new Map<string, ChannelGroup>()
  for (const kind of props.protocols) {
    for (const channel of props.channelsByKind[kind] || []) {
      if (!isSelectable(channel)) continue
      if (!matchesSearch(channel)) continue
      const poolName = poolNameOf(kind, channel.poolId)
      const key = `${kind}:${channel.poolId || ''}`
      const group = map.get(key) || {
        key,
        title: `${poolName} · ${evalProtocolLabel(kind)}`,
        channels: []
      }
      group.channels.push(channel)
      map.set(key, group)
    }
  }
  return [...map.values()]
})

const filteredChannels = computed(() => groups.value.flatMap(group => group.channels))

const isSelected = (channelId: string) => props.modelValue.includes(channelId)

const toggleChannel = (channelId: string) => {
  if (isSelected(channelId)) {
    emitSelection(props.modelValue.filter(id => id !== channelId))
    return
  }
  emitSelection([...props.modelValue, channelId])
}

const isGroupFullySelected = (group: ChannelGroup) =>
  group.channels.length > 0 && group.channels.every(channel => isSelected(channel.id))

const toggleGroup = (group: ChannelGroup) => {
  const groupIds = group.channels.map(channel => channel.id)
  if (isGroupFullySelected(group)) {
    emitSelection(props.modelValue.filter(id => !groupIds.includes(id)))
    return
  }
  const merged = new Set([...props.modelValue, ...groupIds])
  emitSelection([...merged])
}

/** 全选/只选活跃都只作用于当前筛选结果，不动筛选之外已勾的渠道。 */
const mergeSelection = (ids: string[]) => {
  const merged = new Set([...props.modelValue, ...ids])
  emitSelection([...merged])
}

const selectAllFiltered = () => mergeSelection(filteredChannels.value.map(channel => channel.id))

const selectActiveFiltered = () =>
  mergeSelection(
    filteredChannels.value
      .filter(channel => channel.status !== 'suspended')
      .map(channel => channel.id)
  )

// ===== 逐渠道模型选择 =====

const channelModelOf = (channelId: string): string => (props.channelModels || {})[channelId] || ''

const modelOptionsOf = (channelId: string): string[] => modelOptions.value[channelId] || []

const modelLoadingOf = (channelId: string): boolean => !!modelLoading.value[channelId]

const updateChannelModel = (channelId: string, value: unknown) => {
  const next = { ...(props.channelModels || {}) }
  const model = typeof value === 'string' ? value.trim() : ''
  if (model) {
    next[channelId] = model
  } else {
    delete next[channelId]
  }
  emit('update:channelModels', next)
}

const channelById = (channelId: string): (Channel & { id: string }) | undefined => {
  for (const kind of props.protocols) {
    const found = (props.channelsByKind[kind] || []).find(channel => channel.id === channelId)
    if (found && isSelectable(found)) return found
  }
  return undefined
}

/** 聚焦某个渠道的模型框时，探测一次上游可用模型（复用编辑渠道的 discover 逻辑）。 */
const loadModels = async (channelId: string) => {
  const channel = channelById(channelId)
  if (!channel || modelOptions.value[channelId]) return

  const baseUrl = channel.baseUrl?.trim() || channel.baseUrls?.find(url => url.trim())?.trim() || ''
  if (!baseUrl) return

  modelLoading.value = { ...modelLoading.value, [channelId]: true }
  try {
    const response = await api.discoverUpstreamModels({
      baseUrl,
      baseUrls: channel.baseUrls,
      apiKey: channel.apiKeys?.[0] || '',
      serviceType: channel.serviceType,
      insecureSkipVerify: channel.insecureSkipVerify,
      proxyMode: channel.proxyMode,
      proxyUrl: channel.proxyUrl
    })
    const discovered = response.data
      .map(item => item.id?.trim())
      .filter((model): model is string => !!model)
    const merged = Array.from(
      new Set(([channel.defaultModel, ...discovered]).filter((model): model is string => !!model))
    )
    if (merged.length) {
      modelOptions.value = { ...modelOptions.value, [channelId]: merged }
    }
  } catch {
    // 探测失败不阻塞用户：仍可手动输入模型名。
  } finally {
    modelLoading.value = { ...modelLoading.value, [channelId]: false }
  }
}
</script>

<style scoped>
.pool-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}

.pool-card {
  border: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  border-radius: 8px;
  padding: 8px 8px 10px;
}

.pool-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 4px 6px;
}

.channel-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border-radius: 6px;
  cursor: pointer;
}

.channel-row:hover,
.channel-row:focus-visible {
  background: rgba(var(--v-theme-on-surface), 0.06);
  outline: none;
}

.channel-row--selected {
  background: rgba(var(--v-theme-primary), 0.1);
}

.channel-name {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 0.875rem;
}

.channel-model {
  flex: 0 1 200px;
  min-width: 0;
  max-width: 200px;
}
</style>
