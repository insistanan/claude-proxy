<template>
  <section class="pool-section">
    <header class="section-header">
      <div>
        <div class="text-subtitle-2 font-weight-bold">模型路由子池</div>
        <div class="text-caption text-medium-emphasis">按最长模型名称匹配选择子池，再在池内执行故障转移</div>
      </div>
      <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-plus" @click="openCreateDialog">
        新建子池
      </v-btn>
    </header>

    <v-progress-linear v-if="loading" indeterminate color="primary" class="mb-3" />
    <div v-else class="pool-grid">
      <div v-for="(column, columnIndex) in poolColumns" :key="columnIndex" class="pool-column">
      <section
        v-for="{ pool, order } in column"
        :key="pool.id"
        class="pool-card"
        :style="{ order }"
      >
        <header class="pool-card-header">
          <div class="min-width-0">
            <div class="pool-name">{{ pool.name }}</div>
            <div class="pool-rule">匹配：{{ pool.modelMatcher }}</div>
          </div>
          <div class="d-flex align-center ga-1">
            <v-chip size="x-small" color="primary" variant="tonal">{{ pool.channels.length }}</v-chip>
            <v-btn icon size="x-small" variant="text" title="编辑子池" @click="openEditDialog(pool)">
              <v-icon size="small">mdi-pencil</v-icon>
            </v-btn>
            <v-btn
              v-if="pool.id !== 'default'"
              icon
              size="x-small"
              color="error"
              variant="text"
              title="删除子池"
              :disabled="hasAssignedChannels(pool.id)"
              @click="openDeletePoolDialog(pool)"
            >
              <v-icon size="small">mdi-delete</v-icon>
            </v-btn>
          </div>
        </header>
        <draggable
          :list="pool.channels"
          item-key="id"
          handle=".drag-handle"
          :group="{ name: `channel-pools-${channelType}`, pull: true, put: true }"
          :animation="150"
          :scroll="true"
          :scroll-sensitivity="90"
          :scroll-speed="18"
          :empty-insert-threshold="48"
          ghost-class="ghost"
          chosen-class="chosen"
          drag-class="dragging"
          class="pool-channel-list"
          :force-fallback="true"
          @change="scheduleLayoutSave"
        >
          <template #item="{ element, index }">
            <div class="pool-drag-item">
              <slot
                name="channel"
                :channel="element"
                :index="index"
                :pool="pool"
                :move_to_top="() => moveChannel(pool, index, 0)"
                :move_to_bottom="() => moveChannel(pool, index, pool.channels.length - 1)"
              >
                <div class="pool-channel-row" @click="emit('edit', element)">
                  <span class="drag-handle"><v-icon size="small">mdi-drag-vertical</v-icon></span>
                  <span class="pool-index">{{ index + 1 }}</span>
                  <ChannelStatusBadge :status="element.status || 'active'" />
                  <div class="pool-channel-main">
                    <span class="font-weight-medium">{{ element.name }}</span>
                    <span class="text-caption text-medium-emphasis">{{ element.serviceType }}</span>
                  </div>
                </div>
              </slot>
            </div>
          </template>
        </draggable>
        <div v-if="pool.channels.length === 0" class="pool-empty">暂无渠道，可从其他子池拖入</div>
      </section>
      </div>
    </div>
  </section>

  <section class="vision-pool-section">
    <header class="vision-pool-header">
      <div class="d-flex align-center ga-2">
        <v-icon size="small" color="primary">mdi-image-search-outline</v-icon>
        <span class="text-subtitle-2">公用纯图片理解池</span>
        <v-chip size="x-small">{{ visionChannels.length }}</v-chip>
      </div>
      <span class="text-caption text-medium-emphasis">仅供图片理解层调用，不参与常规对话调度</span>
    </header>
    <div class="vision-channel-list">
      <div v-for="channel in visionChannels" :key="channel.index" class="vision-channel-row" @click="emit('edit', channel)">
        <v-icon size="small" color="primary">mdi-image-search-outline</v-icon>
        <ChannelStatusBadge :status="channel.status || 'active'" />
        <span class="font-weight-medium text-truncate">{{ channel.name }}</span>
        <span class="text-caption text-medium-emphasis">{{ channel.serviceType }}</span>
        <v-spacer />
        <v-chip size="x-small" variant="outlined">
          <v-icon start size="x-small">mdi-key</v-icon>{{ channel.apiKeys?.length || 0 }}
        </v-chip>
        <v-btn icon size="x-small" color="error" variant="text" title="删除渠道" @click.stop="emit('delete', channel.index)">
          <v-icon size="small">mdi-delete</v-icon>
        </v-btn>
      </div>
      <div v-if="visionChannels.length === 0" class="pool-empty">暂无公用图片理解渠道</div>
    </div>
  </section>

  <v-dialog v-model="poolDialog" max-width="460">
    <v-card rounded="lg">
      <v-card-title>{{ editingPool ? '编辑子池' : '新建子池' }}</v-card-title>
      <v-card-text class="pt-3">
        <v-text-field v-model="poolName" label="名称" variant="outlined" density="compact" autofocus />
        <v-combobox
          v-model="poolMatcher"
          label="模型名称包含"
          :items="matcherOptions"
          variant="outlined"
          density="compact"
          hint="可选择内置规则或直接输入；具体规则优先于 *，多个具体规则命中时选择最长规则"
          persistent-hint
          @keyup.enter="savePool"
        />
      </v-card-text>
      <v-card-actions>
        <v-spacer />
        <v-btn variant="text" @click="poolDialog = false">取消</v-btn>
        <v-btn color="primary" :loading="saving" :disabled="!poolName.trim() || !normalizedPoolMatcher" @click="savePool">保存</v-btn>
      </v-card-actions>
    </v-card>
  </v-dialog>

  <v-dialog v-model="deletePoolDialog" max-width="460">
    <v-card rounded="lg">
      <v-card-title>删除子池</v-card-title>
      <v-card-text>确定删除“{{ deletingPool?.name }}”吗？只有空子池可以删除。</v-card-text>
      <v-card-actions>
        <v-spacer />
        <v-btn variant="text" @click="deletePoolDialog = false">取消</v-btn>
        <v-btn color="error" :loading="saving" @click="deletePool">删除</v-btn>
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import draggable from 'vuedraggable'
import { api, type Channel, type ChannelPool } from '../services/api'
import ChannelStatusBadge from './ChannelStatusBadge.vue'

const props = defineProps<{
  channels: Channel[]
  channelType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
}>()
const emit = defineEmits<{
  edit: [channel: Channel]
  delete: [channelId: number]
  error: [message: string]
  success: [message: string]
  refresh: []
}>()

const pools = ref<ChannelPool[]>([])
const loading = ref(false)
const saving = ref(false)
const layoutSaving = ref(false)
const poolDialog = ref(false)
const deletePoolDialog = ref(false)
const editingPool = ref<ChannelPool | null>(null)
const deletingPool = ref<ChannelPool | null>(null)
const poolName = ref('')
const poolMatcher = ref<string | null>('')
type PoolView = ChannelPool & { channels: Channel[] }
const poolViews = ref<PoolView[]>([])
let layoutSaveTimer: ReturnType<typeof setTimeout> | null = null
let layoutSavePending = false

const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)
const normalizedPoolMatcher = computed(() => String(poolMatcher.value ?? '').trim())

const loadPools = async () => {
  loading.value = true
  try {
    const response = await api.getChannelPools(props.channelType)
    pools.value = response.pools || []
  } catch (error) {
    pools.value = []
    emit('error', `加载渠道子池失败：${errorMessage(error)}`)
  } finally {
    loading.value = false
  }
}

onMounted(() => void loadPools())
onBeforeUnmount(() => {
  if (layoutSaveTimer) clearTimeout(layoutSaveTimer)
})
watch(() => props.channelType, () => void loadPools())

const syncPoolViews = () => {
  poolViews.value = pools.value.map(pool => ({
    ...pool,
    channels: props.channels
      .filter(channel => !channel.excludeFromConversation && (channel.poolId || 'default') === pool.id && ['active', 'suspended'].includes(channel.status || 'active'))
      .sort((left, right) => (left.priority || left.index + 1) - (right.priority || right.index + 1))
  }))
}
watch([pools, () => props.channels], syncPoolViews, { deep: true })

const conversationPools = computed(() => poolViews.value)
const poolColumns = computed(() => {
  const orderedPools = conversationPools.value.map((pool, order) => ({ pool, order }))
  return [
    orderedPools.filter(({ order }) => order % 2 === 0),
    orderedPools.filter(({ order }) => order % 2 === 1)
  ]
})
const visionChannels = computed(() => props.channels.filter(channel => channel.excludeFromConversation && channel.status !== 'deleted'))
const hasAssignedChannels = (poolId: string) => props.channels.some(channel => channel.status !== 'deleted' && (channel.poolId || 'default') === poolId)

const matcherOptions = computed(() => {
  if (props.channelType === 'messages') return ['*', 'claude']
  if (props.channelType === 'responses') return ['*', 'gpt']
  if (props.channelType === 'gemini') return ['*', 'gemini']
  return ['*']
})

const saveLayout = async () => {
  if (layoutSaving.value) {
    layoutSavePending = true
    return
  }
  layoutSaving.value = true
  let saved = false
  let failed = false
  try {
    const layout = poolViews.value.map(pool => ({
      poolId: pool.id,
      channelIds: pool.channels.map(channel => {
        if (!channel.id) throw new Error(`渠道 ${channel.name} 缺少稳定 ID`)
        return channel.id
      })
    }))
    await api.saveChannelPoolLayout(props.channelType, layout)
    saved = true
    emit('success', '子池故障转移顺序已保存')
  } catch (error) {
    failed = true
    syncPoolViews()
    emit('error', `保存子池布局失败：${errorMessage(error)}`)
  } finally {
    layoutSaving.value = false
    if (failed) {
      layoutSavePending = false
      if (layoutSaveTimer) {
        clearTimeout(layoutSaveTimer)
        layoutSaveTimer = null
      }
      emit('refresh')
    } else if (layoutSavePending) {
      layoutSavePending = false
      scheduleLayoutSave()
    } else if (saved) {
      emit('refresh')
    }
  }
}

const scheduleLayoutSave = () => {
  if (layoutSaveTimer) clearTimeout(layoutSaveTimer)
  layoutSaveTimer = setTimeout(() => {
    layoutSaveTimer = null
    void saveLayout()
  }, 0)
}

const moveChannel = (pool: PoolView, from: number, to: number) => {
  if (from === to) return
  const [channel] = pool.channels.splice(from, 1)
  pool.channels.splice(to, 0, channel)
  scheduleLayoutSave()
}

const openCreateDialog = () => {
  editingPool.value = null
  poolName.value = ''
  poolMatcher.value = ''
  poolDialog.value = true
}

const openEditDialog = (pool: ChannelPool) => {
  editingPool.value = pool
  poolName.value = pool.name
  poolMatcher.value = pool.modelMatcher
  poolDialog.value = true
}

const savePool = async () => {
  const name = poolName.value.trim()
  const modelMatcher = normalizedPoolMatcher.value
  if (!name || !modelMatcher) return
  saving.value = true
  try {
    if (editingPool.value) {
      await api.updateChannelPool(props.channelType, editingPool.value.id, { name, modelMatcher })
      emit('success', '子池配置已更新')
    } else {
      await api.createChannelPool(props.channelType, { name, modelMatcher })
      emit('success', '子池已创建')
    }
    poolDialog.value = false
    await loadPools()
  } catch (error) {
    emit('error', `保存子池失败：${errorMessage(error)}`)
  } finally {
    saving.value = false
  }
}

const openDeletePoolDialog = (pool: ChannelPool) => {
  deletingPool.value = pool
  deletePoolDialog.value = true
}

const deletePool = async () => {
  if (!deletingPool.value) return
  saving.value = true
  try {
    await api.deleteChannelPool(props.channelType, deletingPool.value.id)
    deletePoolDialog.value = false
    emit('success', '子池已删除')
    await loadPools()
  } catch (error) {
    emit('error', `删除子池失败：${errorMessage(error)}`)
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.pool-section { padding-top: 14px; }
.section-header, .vision-pool-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 10px; }
.pool-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; align-items: start; }
.pool-column { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.pool-card { border: 1px solid rgba(var(--v-theme-on-surface), .42); background: rgb(var(--v-theme-surface)); min-width: 0; container-type: inline-size; }
.pool-card-header { min-height: 52px; display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 9px 10px; border-bottom: 1px solid rgba(var(--v-theme-on-surface), .42); background: rgba(var(--v-theme-primary), .06); }
.pool-name { font-weight: 700; line-height: 1.2; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-rule { margin-top: 3px; font-size: 12px; color: rgba(var(--v-theme-on-surface), .58); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-channel-list { padding: 8px; display: flex; flex-direction: column; gap: 7px; min-height: 68px; }
.pool-drag-item { min-width: 0; }
.ghost { opacity: .45; background: rgba(var(--v-theme-primary), .14); }
.chosen { cursor: grabbing; }
.dragging { opacity: .9; }
.drag-handle { cursor: grab; }
.pool-channel-row, .vision-channel-row { display: flex; align-items: center; gap: 7px; min-width: 0; padding: 7px 8px; border: 1px solid rgba(var(--v-theme-on-surface), .32); cursor: pointer; }
.pool-channel-row:hover, .vision-channel-row:hover { background: rgba(var(--v-theme-primary), .07); }
.pool-index { display: inline-grid; place-items: center; width: 22px; height: 22px; flex: 0 0 auto; background: rgb(var(--v-theme-primary)); color: rgb(var(--v-theme-on-primary)); font-size: 11px; font-weight: 700; }
.pool-channel-main { min-width: 0; flex: 1; display: flex; align-items: baseline; gap: 7px; overflow: hidden; }
.pool-channel-main span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-empty { display: grid; place-items: center; min-height: 48px; color: rgba(var(--v-theme-on-surface), .55); font-size: 13px; }
.vision-pool-section { margin-top: 18px; padding-bottom: 14px; border-bottom: 1px solid rgba(var(--v-theme-on-surface), .22); }
.vision-channel-list { display: flex; flex-direction: column; gap: 7px; padding: 8px; border: 1px dashed rgba(var(--v-theme-on-surface), .48); }
.min-width-0 { min-width: 0; }
@media (max-width: 1400px) {
  .pool-grid { display: flex; flex-direction: column; }
  .pool-column { display: contents; }
}
@media (max-width: 900px) {
  .section-header, .vision-pool-header { align-items: flex-start; flex-direction: column; }
  .pool-channel-row, .vision-channel-row { flex-wrap: wrap; }
}
</style>
