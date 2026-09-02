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

    <div class="text-caption text-medium-emphasis mb-3">
      <v-icon size="small" class="mr-1">mdi-help-circle-outline</v-icon>
      渠道已按分组名称字母正序排列，组内按优先级由高到低排序；可单独指定模型与思考等级。已选 {{ modelValue.length }} 个渠道。
    </div>

    <div v-if="!groupedRows.length" class="text-body-2 text-medium-emphasis py-4 text-center">
      没有符合条件的可用渠道
    </div>

    <div v-else class="groups-container">
      <v-card
        v-for="group in groupedRows"
        :key="group.key"
        variant="outlined"
        class="mb-3 group-card"
      >
        <div
          class="group-header pa-2 px-3 d-flex align-center ga-2"
          role="button"
          tabindex="0"
          @click="toggleGroupCollapse(group.key)"
          @keydown.enter.prevent="toggleGroupCollapse(group.key)"
          @keydown.space.prevent="toggleGroupCollapse(group.key)"
        >
          <v-chip size="x-small" variant="tonal" color="primary" class="font-weight-medium">
            {{ evalProtocolLabel(group.kind) }}
          </v-chip>
          <span class="text-subtitle-2 font-weight-bold">{{ group.poolName }}</span>
          <v-chip size="x-small" variant="outlined" color="grey">
            {{ group.channels.length }} 个渠道
          </v-chip>
          <v-spacer />
          <v-btn
            size="x-small"
            variant="text"
            @click.stop="toggleGroupSelection(group)"
          >
            {{ isGroupAllSelected(group) ? '取消组内全选' : '选择组内全部' }}
          </v-btn>
          <v-btn
            icon
            size="x-small"
            variant="text"
            class="group-collapse-btn ml-1"
            :title="isGroupCollapsed(group.key) ? '展开分组' : '折叠分组'"
            @click.stop="toggleGroupCollapse(group.key)"
          >
            <v-icon
              size="18"
              icon="mdi-chevron-down"
              class="group-collapse-icon"
              :class="{ 'group-collapse-icon--collapsed': isGroupCollapsed(group.key) }"
            />
          </v-btn>
        </div>

        <v-expand-transition>
          <div v-show="!isGroupCollapsed(group.key)" class="picker-table-wrap">
            <table class="picker-table">
              <thead>
                <tr>
                  <th class="col-check"></th>
                  <th class="col-priority">优先级</th>
                  <th class="col-channel">渠道</th>
                  <th class="col-model">模型</th>
                  <th class="col-thinking">思考等级</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="channel in group.channels"
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
                  <td class="col-priority">
                    <span class="text-caption text-medium-emphasis">P{{ channel.priority ?? (channel.index + 1) }}</span>
                    <v-chip v-if="channel.status === 'suspended'" size="x-small" variant="tonal" color="grey" class="ml-1">熔断</v-chip>
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
        </v-expand-transition>
      </v-card>
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

interface ChannelGroup {
  key: string
  kind: ApiTab
  poolId: string
  poolName: string
  channels: PickerRow[]
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
const collapsedGroups = ref<Record<string, boolean>>({})
const modelOptions = ref<Record<string, string[]>>({})
const modelLoading = ref<Record<string, boolean>>({})

const isGroupCollapsed = (groupKey: string): boolean => !!collapsedGroups.value[groupKey]

const toggleGroupCollapse = (groupKey: string) => {
  collapsedGroups.value = {
    ...collapsedGroups.value,
    [groupKey]: !collapsedGroups.value[groupKey]
  }
}

const emitSelection = (ids: string[]) => emit('update:modelValue', ids)

const toggleProtocol = (kind: ApiTab) => {
  const next = props.protocols.includes(kind)
    ? props.protocols.filter(item => item !== kind)
    : [...props.protocols, kind]
  emit('update:protocols', next)
}

/** 备用池 (disabled)、弃用池 (deprecated)、已删除 (deleted) 不展示。 */
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

/** 获取指定协议下 poolId 对应的名称。 */
const poolNameOf = (kind: ApiTab, poolId?: string): string => {
  const targetId = poolId || 'default'
  if (targetId === 'default') return 'default'
  const poolList = props.poolsByKind[kind] || []
  const found = poolList.find(p => p.id === targetId)
  return found?.name || targetId
}

/**
 * 按照协议 + 分组 (Pool) 进行分类：
 * 1. 分组名按英文字母正序排序 (a-z)；
 * 2. 组内渠道按照现有优先级 (priority / index) 顺序排序。
 */
const groupedRows = computed<ChannelGroup[]>(() => {
  const groupsMap = new Map<string, ChannelGroup>()

  for (const kind of props.protocols) {
    for (const channel of props.channelsByKind[kind] || []) {
      if (!isSelectable(channel)) continue
      if (!matchesSearch(channel)) continue

      const poolId = channel.poolId || 'default'
      const poolName = poolNameOf(kind, poolId)
      const groupKey = `${kind}:${poolId}`

      let group = groupsMap.get(groupKey)
      if (!group) {
        group = {
          key: groupKey,
          kind,
          poolId,
          poolName,
          channels: []
        }
        groupsMap.set(groupKey, group)
      }
      group.channels.push({ ...channel, kind })
    }
  }

  // 1. 每个分组内部的渠道按优先级 (priority 升序，而后 index 升序) 排列
  for (const group of groupsMap.values()) {
    group.channels.sort((left, right) => {
      const pLeft = left.priority ?? (left.index + 1)
      const pRight = right.priority ?? (right.index + 1)
      if (pLeft !== pRight) return pLeft - pRight
      return left.index - right.index
    })
  }

  // 2. 分组按照 poolName 英文字母正序 (a-z) 排序，如果名称相同按协议排序
  const groupsList = Array.from(groupsMap.values())
  groupsList.sort((left, right) => {
    const nameCmp = left.poolName.localeCompare(right.poolName, 'en', { sensitivity: 'base' })
    if (nameCmp !== 0) return nameCmp
    return left.kind.localeCompare(right.kind)
  })

  return groupsList
})

/** 拍平的所有当前筛选可见的渠道列表 */
const filteredChannels = computed<PickerRow[]>(() =>
  groupedRows.value.flatMap(group => group.channels)
)

const isSelected = (channelId: string) => props.modelValue.includes(channelId)

const toggleChannel = (channelId: string) => {
  if (isSelected(channelId)) {
    emitSelection(props.modelValue.filter(id => id !== channelId))
    return
  }
  emitSelection([...props.modelValue, channelId])
}

const isGroupAllSelected = (group: ChannelGroup): boolean => {
  if (!group.channels.length) return false
  return group.channels.every(ch => isSelected(ch.id))
}

const toggleGroupSelection = (group: ChannelGroup) => {
  const groupIds = group.channels.map(ch => ch.id)
  if (isGroupAllSelected(group)) {
    emitSelection(props.modelValue.filter(id => !groupIds.includes(id)))
  } else {
    const merged = new Set([...props.modelValue, ...groupIds])
    emitSelection([...merged])
  }
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
  filteredChannels.value.find(channel => channel.id === channelId)

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
.groups-container {
  max-height: 520px;
  overflow-y: auto;
}

.group-card {
  background: rgba(var(--v-theme-surface), 0.6);
  border-color: rgba(var(--v-theme-on-surface), 0.1);
}

.group-header {
  background: rgba(var(--v-theme-on-surface), 0.03);
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.08);
  cursor: pointer;
  user-select: none;
}

.group-header:hover {
  background: rgba(var(--v-theme-on-surface), 0.06);
}

.group-collapse-btn {
  width: 24px;
  height: 24px;
}

.group-collapse-icon {
  transition: transform 0.28s cubic-bezier(0.4, 0, 0.2, 1);
}

.group-collapse-icon--collapsed {
  transform: rotate(-90deg);
}

.picker-table-wrap {
  overflow-x: auto;
}

.picker-table {
  border-collapse: collapse;
  width: 100%;
}

.picker-table th,
.picker-table td {
  padding: 6px 10px;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.06);
  vertical-align: middle;
}

.picker-table thead th {
  background: rgb(var(--v-theme-surface));
  font-size: 0.75rem;
  font-weight: 600;
  color: rgba(var(--v-theme-on-surface), 0.6);
  text-align: left;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.12);
}

.picker-table tbody tr {
  cursor: pointer;
  transition: background 0.1s;
}

.picker-table tbody tr:hover {
  background: rgba(var(--v-theme-on-surface), 0.04);
}

.picker-table tbody tr.row--selected {
  background: rgba(var(--v-theme-primary), 0.08);
}

.col-check {
  width: 36px;
}

.col-priority {
  width: 90px;
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
