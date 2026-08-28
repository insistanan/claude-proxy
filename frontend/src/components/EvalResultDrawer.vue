<template>
  <v-navigation-drawer :model-value="modelValue" location="end" temporary width="560" @update:model-value="emit('update:modelValue', $event)">
    <div v-if="selection" class="pa-4">
      <div class="d-flex align-center ga-2 mb-1">
        <v-chip size="small" variant="tonal" :color="evalVerdictColor(result?.verdict)">
          {{ evalVerdictLabel(result?.verdict) }}
        </v-chip>
        <div class="text-body-1 font-weight-medium text-truncate">{{ selection.probe.name }}</div>
        <v-spacer />
        <v-btn icon="mdi-close" size="small" variant="text" @click="emit('update:modelValue', false)" />
      </div>

      <div class="text-caption text-medium-emphasis mb-4">
        {{ selection.channelName }} · {{ selection.probe.category === 'authenticity' ? '真伪' : '智商' }} ·
        判定 {{ selection.probe.judge.kind }} · 用时 {{ result?.latencyMs ?? 0 }} ms ·
        {{ evalFormatTime(result?.createdAt) }}
      </div>

      <v-alert v-if="reason" type="info" variant="tonal" density="compact" class="mb-4">{{ reason }}</v-alert>
      <v-alert v-if="evidence" type="info" variant="tonal" density="compact" class="mb-4">
        证据：{{ evidence }}
      </v-alert>

      <template v-if="twins.length">
        <div class="text-body-2 font-weight-medium mb-1">随机数指纹相近</div>
        <div class="text-caption text-medium-emphasis mb-2">
          只是提示，不改结论：同源官方中转也会相近，需要人看一眼。
        </div>
        <v-list density="compact" class="mb-4 pa-0" bg-color="transparent">
          <v-list-item v-for="twin in twins" :key="twin.channelId" class="px-0">
            <v-list-item-title class="text-body-2">{{ channelNameOf(twin.channelId) }}</v-list-item-title>
            <template #append>
              <span class="text-caption text-medium-emphasis">相似度 {{ twin.similarity.toFixed(3) }}</span>
            </template>
          </v-list-item>
        </v-list>
      </template>

      <template v-if="svg">
        <div class="text-body-2 font-weight-medium mb-2">SVG 预览</div>
        <div class="svg-frame mb-4">
          <img :src="evalSvgPreviewUrl(svg)" alt="渠道返回的 SVG" />
        </div>
      </template>

      <template v-if="judgeText">
        <div class="text-body-2 font-weight-medium mb-2">评审原文（被测渠道自评）</div>
        <pre class="excerpt mb-4">{{ judgeText }}</pre>
      </template>

      <template v-if="excerpt">
        <div class="text-body-2 font-weight-medium mb-2">回答摘录</div>
        <pre class="excerpt mb-4">{{ excerpt }}</pre>
      </template>

      <template v-if="numbersSummary">
        <div class="text-body-2 font-weight-medium mb-2">样本数字</div>
        <pre class="excerpt mb-4">{{ numbersSummary }}</pre>
      </template>

      <template v-if="otherDetails.length">
        <div class="text-body-2 font-weight-medium mb-2">判定细节</div>
        <table class="detail-table">
          <tbody>
            <tr v-for="item in otherDetails" :key="item.key">
              <td class="detail-key">{{ item.key }}</td>
              <td>{{ item.text }}</td>
            </tr>
          </tbody>
        </table>
      </template>

      <div class="text-body-2 font-weight-medium mt-4 mb-2">题面</div>
      <pre class="excerpt">{{ selection.probe.stimulus.prompt }}</pre>
    </div>
  </v-navigation-drawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { EvalCellSelection } from '@/utils/eval'
import { evalFormatTime, evalSvgPreviewUrl, evalVerdictColor, evalVerdictLabel } from '@/utils/eval'

const props = defineProps<{
  modelValue: boolean
  selection: EvalCellSelection | null
  /** 指纹相近的对方渠道用它换成人看得懂的名字 */
  channelNameOf: (channelId: string) => string
}>()

const emit = defineEmits<{
  'update:modelValue': [boolean]
}>()

/** 这些键单独渲染，不再进"判定细节"表 */
const RENDERED_KEYS = ['reason', 'reasons', 'evidence', 'fingerprintTwins', 'judgeText', 'candidate', 'numbers']

const result = computed(() => props.selection?.result)
const detail = computed<Record<string, unknown>>(() => result.value?.detail || {})
const excerpt = computed(() => result.value?.excerpt || '')

const reason = computed(() => {
  if (typeof detail.value.reason === 'string' && detail.value.reason) return detail.value.reason
  const reasons = detail.value.reasons
  if (Array.isArray(reasons)) return reasons.map(item => String(item)).join('；')
  if (typeof reasons === 'string') return reasons
  return ''
})
const evidence = computed(() => (typeof detail.value.evidence === 'string' ? detail.value.evidence : ''))
const judgeText = computed(() => (typeof detail.value.judgeText === 'string' ? detail.value.judgeText : ''))

const twins = computed(() => {
  const raw = detail.value.fingerprintTwins
  if (!Array.isArray(raw)) return []
  return raw
    .filter((item): item is { channelId: string; similarity: number } =>
      !!item && typeof item === 'object' && typeof (item as { channelId?: unknown }).channelId === 'string'
    )
    .map(item => ({ channelId: item.channelId, similarity: Number(item.similarity) || 0 }))
})

/** 只有整段 SVG 才预览；截断的片段渲染出来是坏图，不如只看文本。 */
const svg = computed(() => {
  const raw = excerpt.value
  const start = raw.indexOf('<svg')
  const end = raw.lastIndexOf('</svg>')
  if (start < 0 || end <= start) return ''
  return raw.slice(start, end + '</svg>'.length)
})

const numbersSummary = computed(() => {
  const raw = detail.value.numbers
  if (!Array.isArray(raw) || !raw.length) return ''
  const head = raw.slice(0, 40).join(', ')
  return raw.length > 40 ? `${head} …（共 ${raw.length} 个）` : `${head}（共 ${raw.length} 个）`
})

const formatValue = (value: unknown): string => {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'number') return Number.isInteger(value) ? String(value) : value.toFixed(3)
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'string') return value
  return JSON.stringify(value)
}

const otherDetails = computed(() =>
  Object.entries(detail.value)
    .filter(([key]) => !RENDERED_KEYS.includes(key))
    .map(([key, value]) => ({ key, text: formatValue(value) }))
)
</script>

<style scoped>
.excerpt {
  margin: 0;
  padding: 10px 12px;
  border-radius: 6px;
  background: rgba(var(--v-theme-on-surface), 0.06);
  font-size: 0.8125rem;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow-y: auto;
}

.svg-frame {
  border: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  border-radius: 6px;
  padding: 12px;
  display: flex;
  justify-content: center;
  background: #fff;
}

.svg-frame img {
  max-width: 100%;
  max-height: 220px;
}

.detail-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8125rem;
}

.detail-table td {
  padding: 4px 8px;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.08);
  word-break: break-word;
}

.detail-key {
  width: 40%;
  color: rgba(var(--v-theme-on-surface), 0.6);
}
</style>
