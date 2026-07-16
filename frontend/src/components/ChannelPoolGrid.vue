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
      <section v-for="pool in conversationPools" :key="pool.id" class="pool-card">
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
        <div class="pool-channel-list">
          <div v-for="(channel, index) in pool.channels" :key="channel.index" class="pool-channel-row" @click="emit('edit', channel)">
            <span class="pool-index">{{ index + 1 }}</span>
            <ChannelStatusBadge :status="channel.status || 'active'" />
            <div class="pool-channel-main">
              <span class="font-weight-medium">{{ channel.name }}</span>
              <span class="text-caption text-medium-emphasis">{{ channel.serviceType }}</span>
            </div>
            <v-chip v-if="channel.visionCapable" size="x-small" color="primary" variant="tonal">图片</v-chip>
            <v-chip size="x-small" variant="outlined">
              <v-icon start size="x-small">mdi-key</v-icon>{{ channel.apiKeys?.length || 0 }}
            </v-chip>
            <v-btn icon size="x-small" color="error" variant="text" title="删除渠道" @click.stop="emit('delete', channel.index)">
              <v-icon size="small">mdi-delete</v-icon>
            </v-btn>
          </div>
          <div v-if="pool.channels.length === 0" class="pool-empty">暂无渠道</div>
        </div>
      </section>
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
        <v-text-field
          v-model="poolMatcher"
          label="模型名称包含"
          variant="outlined"
          density="compact"
          :disabled="editingPool?.id === 'default'"
          :hint="editingPool?.id === 'default' ? '默认子池固定使用 * 兜底' : '不区分大小写；多个规则同时命中时选择最长规则'"
          persistent-hint
          @keyup.enter="savePool"
        />
      </v-card-text>
      <v-card-actions>
        <v-spacer />
        <v-btn variant="text" @click="poolDialog = false">取消</v-btn>
        <v-btn color="primary" :loading="saving" :disabled="!poolName.trim() || !poolMatcher.trim()" @click="savePool">保存</v-btn>
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
import { computed, onMounted, ref, watch } from 'vue'
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
}>()

const pools = ref<ChannelPool[]>([])
const loading = ref(false)
const saving = ref(false)
const poolDialog = ref(false)
const deletePoolDialog = ref(false)
const editingPool = ref<ChannelPool | null>(null)
const deletingPool = ref<ChannelPool | null>(null)
const poolName = ref('')
const poolMatcher = ref('')

const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)

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
watch(() => props.channelType, () => void loadPools())

const conversationPools = computed(() => pools.value.map(pool => ({
  ...pool,
  channels: props.channels.filter(channel => !channel.excludeFromConversation && (channel.poolId || 'default') === pool.id && channel.status !== 'deleted')
})))
const visionChannels = computed(() => props.channels.filter(channel => channel.excludeFromConversation && channel.status !== 'deleted'))
const hasAssignedChannels = (poolId: string) => props.channels.some(channel => channel.status !== 'deleted' && (channel.poolId || 'default') === poolId)

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
  const modelMatcher = poolMatcher.value.trim()
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
.pool-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.pool-card { border: 1px solid rgba(var(--v-theme-on-surface), .42); background: rgb(var(--v-theme-surface)); min-width: 0; }
.pool-card-header { min-height: 52px; display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 9px 10px; border-bottom: 1px solid rgba(var(--v-theme-on-surface), .42); background: rgba(var(--v-theme-primary), .06); }
.pool-name { font-weight: 700; line-height: 1.2; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-rule { margin-top: 3px; font-size: 12px; color: rgba(var(--v-theme-on-surface), .58); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-channel-list { padding: 8px; display: flex; flex-direction: column; gap: 7px; min-height: 68px; }
.pool-channel-row, .vision-channel-row { display: flex; align-items: center; gap: 7px; min-width: 0; padding: 7px 8px; border: 1px solid rgba(var(--v-theme-on-surface), .32); cursor: pointer; }
.pool-channel-row:hover, .vision-channel-row:hover { background: rgba(var(--v-theme-primary), .07); }
.pool-index { display: inline-grid; place-items: center; width: 22px; height: 22px; flex: 0 0 auto; background: rgb(var(--v-theme-primary)); color: rgb(var(--v-theme-on-primary)); font-size: 11px; font-weight: 700; }
.pool-channel-main { min-width: 0; flex: 1; display: flex; align-items: baseline; gap: 7px; overflow: hidden; }
.pool-channel-main span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pool-empty { display: grid; place-items: center; min-height: 48px; color: rgba(var(--v-theme-on-surface), .55); font-size: 13px; }
.vision-pool-section { margin-top: 18px; padding-bottom: 14px; border-bottom: 1px solid rgba(var(--v-theme-on-surface), .22); }
.vision-channel-list { display: flex; flex-direction: column; gap: 7px; padding: 8px; border: 1px dashed rgba(var(--v-theme-on-surface), .48); }
.min-width-0 { min-width: 0; }
@media (max-width: 900px) {
  .pool-grid { grid-template-columns: 1fr; }
  .section-header, .vision-pool-header { align-items: flex-start; flex-direction: column; }
  .pool-channel-row, .vision-channel-row { flex-wrap: wrap; }
}
</style>
