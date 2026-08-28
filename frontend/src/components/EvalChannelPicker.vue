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

      <v-spacer />

      <v-btn size="small" variant="text" @click="selectAllFiltered">全选筛选</v-btn>
      <v-btn size="small" variant="text" @click="selectActiveFiltered">只选活跃</v-btn>
      <v-btn size="small" variant="text" :disabled="!modelValue.length" @click="emitSelection([])">清空</v-btn>
    </div>

    <div class="text-caption text-medium-emphasis mb-2">
      <v-icon size="small" class="mr-1">mdi-help-circle-outline</v-icon>
      每个渠道可单独指定模型与思考等级（优先于「统一模型 / 统一思考」）；留空则用全局设置。
      模型框聚焦时自动探测上游可用模型。已选 {{ modelValue.length }} 个渠道。
    </div>

    <div v-if="!rows.length" class="text-body-2 text-medium-emphasis py-4">
      没有渠道
    </div>

    <div v-else class="picker-table-wrap">
      <table class="picker-table">
        <thead>
          <tr>
            <th class="col-check"></th>
            <th class="col-protocol">协议</th>
            <th class="col-channel">渠道</th>
            <th class="col-model">模型</th>
            <th class="col-thinking">思考等级</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="channel in rows"
            :key="channel.id"
            :class="{ 'row--selected': isSelected(channel.id) }"
            role="button"
            tabindex="0"
            @click="toggleChannel(channel.id)"
            @keydown.enter.prevent="toggleChannel(channel.id)"
            @keydown.space.prevent="toggleChannel(channel.id)"
          >
            <td class="col-check">
              <v-icon size="18" :color="isSelected(channel.id) ? 'primary' : undefined">
                {{ isSelected(channel.id) ? 'mdi-checkbox-marked' : 'mdi-checkbox-blank-outline' }}
              </v-icon>
            </td>
            <td class="col-protocol">
              <v-chip size="x-small" variant="outlined" color="grey">{{ evalProtocolLabel(channel.kind) }}</v-chip>
              <v-chip v-if="channel.status === 'suspended'" size="x-small" variant="tonal" color="grey">熔断</v-chip>
            </td>
            <td class="col-channel">
              <span class="text-truncate font-weight-medium">{{ channel.name }}</span>
            </td>
            <td class="col-model">
              <v-combobox
                :model-value="channelModelOf(channel.id)"
                :items="modelOptionsOf(channel.id)"
                :loading="modelLoadingOf(channel.id)"
                density="compact"
                variant="outlined"
                hide-details
                placeholder="统一模型"
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
            </td>
            <td class="col-thinking">
              <v-select
                :model-value="channelThinkingOf(channel.id)"
                :items="THINKING_ITEMS"
                item-title="title"
                item-value="value"
                density="compact"
                variant="outlined"
                hide-details
                placeholder="跟随全局"
                clearable
                @click.stop
                @keydown.stop
                @update:model-value="(value: unknown) => updateChannelThinking(channel.id, value)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type ApiTab, type Channel, type ChannelPool } from '@/services/api'
import { EVAL_PROTOCOL_KINDS, EVAL_THINKING_ITEMS, evalProtocolLabel } from '@/utils/eval'

interface PickerRow extends Channel {
  id: string
  kind: ApiTab
}

const THINKING_ITEMS = EVAL_THINKING_ITEMS.filter(item => item.value !== 'inherit')

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
  /** channelId → 渠道专属思考等级覆盖，优先于统一 thinking */
  channelThinking?: Record<string, string>
}>()

const emit = defineEmits<{
  'update:modelValue': [string[]]
  'update:protocols': [ApiTab[]]
  'update:channelModels': [Record<string, string>]
  'update:channelThinking': [Record<string, string>]
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

/**
 * 全部可选渠道拍平成一行一个。按「协议 → 池名」排序分组展示；
 * 不同协议的同名池不合并——同名不代表同一批上游。
 */
const rows = computed<PickerRow[]>(() => {
  const list: PickerRow[] = []
  for (const kind of props.protocols) {
    for (const channel of props.channelsByKind[kind] || []) {
      if (!isSelectable(channel)) continue
      if (!matchesSearch(channel)) continue
      list.push({ ...channel, kind })
    }
  }
  return list
})

const filteredChannels = computed(() => rows.value)

const isSelected = (channelId: string) => props.modelValue.includes(channelId)

const toggleChannel = (channelId: string) => {
  if (isSelected(channelId)) {
    emitSelection(props.modelValue.filter(id => id !== channelId))
    return
  }
  emitSelection([...props.modelValue, channelId])
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

// ===== 逐渠道模型 / 思考等级选择 =====

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

const channelThinkingOf = (channelId: string): string => (props.channelThinking || {})[channelId] || ''

const updateChannelThinking = (channelId: string, value: unknown) => {
  const next = { ...(props.channelThinking || {}) }
  const level = typeof value === 'string' ? value.trim() : ''
  if (level) {
    next[channelId] = level
  } else {
    delete next[channelId]
  }
  emit('update:channelThinking', next)
}

const channelById = (channelId: string): PickerRow | undefined =>
  rows.value.find(channel => channel.id === channelId)

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
.picker-table-wrap {
  max-height: 420px;
  overflow-y: auto;
  border: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  border-radius: 8px;
}

.picker-table {
  border-collapse: collapse;
  width: 100%;
}

.picker-table th,
.picker-table td {
  padding: 4px 8px;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.08);
  vertical-align: middle;
}

.picker-table thead th {
  position: sticky;
  top: 0;
  z-index: 1;
  background: rgb(var(--v-theme-surface));
  font-size: 0.75rem;
  font-weight: 600;
  color: rgba(var(--v-theme-on-surface), 0.6);
  text-align: left;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.15);
}

.picker-table tbody tr {
  cursor: pointer;
  transition: background 0.1s;
}

.picker-table tbody tr:hover {
  background: rgba(var(--v-theme-on-surface), 0.05);
}

.picker-table tbody tr.row--selected {
  background: rgba(var(--v-theme-primary), 0.08);
}

.col-check {
  width: 36px;
}

.col-protocol {
  width: 110px;
  white-space: nowrap;
}

.col-channel {
  width: 240px;
}

.col-channel span {
  display: inline-block;
  max-width: 100%;
}

.col-model {
  width: 240px;
}

.col-thinking {
  width: 150px;
}
</style>
