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
          <span class="text-caption text-medium-emphasis text-truncate">
            {{ channel.defaultModel || '无默认模型' }}
          </span>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ApiTab, Channel, ChannelPool } from '@/services/api'
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
}>()

const emit = defineEmits<{
  'update:modelValue': [string[]]
  'update:protocols': [ApiTab[]]
}>()

const search = ref('')

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
</script>

<style scoped>
.pool-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
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
</style>
