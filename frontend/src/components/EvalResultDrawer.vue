<template>
  <v-navigation-drawer :model-value="modelValue" location="end" temporary width="560" @update:model-value="emit('update:modelValue', $event)">
    <div v-if="selection" class="pa-4">
      <div class="d-flex align-center ga-2 mb-1">
        <v-btn
          v-if="probes && probes.length > 1"
          icon="mdi-chevron-left"
          size="small"
          variant="text"
          :disabled="probeIndex <= 0"
          title="上一题"
          @click="prevProbe"
        />
        <div class="text-body-1 font-weight-medium text-truncate">{{ selection.probe.name }}</div>
        <v-spacer />
        <span v-if="probes && probes.length > 1" class="text-caption text-medium-emphasis">{{ probeIndex + 1 }} / {{ probes.length }}</span>
        <v-btn
          v-if="probes && probes.length > 1"
          icon="mdi-chevron-right"
          size="small"
          variant="text"
          :disabled="probeIndex >= probes.length - 1"
          title="下一题"
          @click="nextProbe"
        />
        <v-btn icon="mdi-close" size="small" variant="text" @click="emit('update:modelValue', false)" />
      </div>

      <div class="text-caption text-medium-emphasis mb-4">
        {{ selection.channelName }} · {{ selection.probe.category === 'authenticity' ? '真伪' : '智商' }} ·
        判定 {{ selection.probe.judge.kind }} · 用时 {{ result?.latencyMs ?? 0 }} ms ·
        {{ evalFormatTime(result?.createdAt) }}
      </div>

      <!-- 题面在顶部：先让人知道这题在测什么 -->
      <div class="section-title">题面</div>
      <pre class="excerpt mb-4">{{ selection.probe.stimulus.prompt }}</pre>

      <template v-if="judgeText">
        <div class="section-title">评审原文（被测渠道自评）</div>
        <pre class="excerpt mb-4">{{ judgeText }}</pre>
      </template>

      <template v-if="excerpt">
        <div class="section-title">回答摘录</div>
        <pre class="excerpt mb-4">{{ excerpt }}</pre>
      </template>

      <template v-if="svg">
        <div class="section-title">SVG 预览</div>
        <div class="svg-frame mb-4">
          <img :src="evalSvgPreviewUrl(svg)" alt="渠道返回的 SVG" />
        </div>
      </template>

      <template v-if="reason">
        <div class="section-title">判定说明</div>
        <v-alert type="info" variant="tonal" density="compact" class="mb-4">{{ reason }}</v-alert>
      </template>

      <template v-if="evidence">
        <div class="section-title">证据</div>
        <v-alert type="info" variant="tonal" density="compact" class="mb-4">证据：{{ evidence }}</v-alert>
      </template>

      <template v-if="twins.length">
        <div class="section-title">随机数指纹相近</div>
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

      <template v-if="numbersSummary">
        <div class="section-title">样本数字</div>
        <pre class="excerpt mb-4">{{ numbersSummary }}</pre>
      </template>

      <template v-if="otherDetails.length">
        <div class="section-title">判定细节</div>
        <table class="detail-table">
          <tbody>
            <tr v-for="item in otherDetails" :key="item.key">
              <td class="detail-key">{{ item.key }}</td>
              <td>{{ item.text }}</td>
            </tr>
          </tbody>
        </table>
      </template>

      <!-- 结论收尾：大号判定 + 耗时 -->
      <div class="verdict-footer">
        <v-chip size="large" variant="flat" :color="evalVerdictColor(result?.verdict)" class="verdict-chip">
          {{ evalVerdictLabel(result?.verdict) }}
        </v-chip>
        <div class="verdict-meta">
          <span v-if="selection.result?.detail?.thinkingOverride" class="text-caption text-medium-emphasis">
            思考 {{ selection.result.detail.thinkingOverride }}
          </span>
          <span class="text-caption text-medium-emphasis">耗时 {{ result?.latencyMs ?? 0 }} ms</span>
        </div>
      </div>
    </div>
  </v-navigation-drawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { EvalProbe, EvalResult } from '@/services/api'
import type { EvalCellSelection } from '@/utils/eval'
import { evalFormatTime, evalSvgPreviewUrl, evalVerdictColor, evalVerdictLabel } from '@/utils/eval'

const props = defineProps<{
  modelValue: boolean
  selection: EvalCellSelection | null
  /** 指纹相近的对方渠道用它换成人看得懂的名字 */
  channelNameOf: (channelId: string) => string
  /** 当前批次的全部探针，按套件顺序；用于上/下一题导航。空则不显示导航。 */
  probes?: EvalProbe[]
  /** 当前批次的结果，用于在导航时按 (channelId, probeId) 查找下一个格子的结果。 */
  results?: EvalResult[]
}>()

const emit = defineEmits<{
  'update:modelValue': [boolean]
  /** 切换探针时，把新的格子上下文抛回去让父组件同步选中态。 */
  navigate: [EvalCellSelection]
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

/** 当前选中探针在 probes 里的下标；没找到返回 -1，导航按钮自动隐藏。 */
const probeIndex = computed(() => {
  if (!props.selection || !props.probes || !props.probes.length) return -1
  return props.probes.findIndex(probe => probe.id === props.selection!.probe.id)
})

const moveProbe = (direction: number) => {
  if (!props.selection || !props.probes || probeIndex.value < 0) return
  const target = props.probes[probeIndex.value + direction]
  if (!target) return
  const found = props.results?.find(
    item => item.channelId === props.selection!.channelId && item.probeId === target.id
  )
  // 没跑过的格子（result 缺失）也要能跳过去看题面，构造一个空 result。
  const fallback: EvalResult = {
    id: '',
    runId: '',
    channelId: props.selection.channelId,
    probeId: target.id,
    verdict: '—',
    latencyMs: 0,
    createdAt: 0
  }
  emit('navigate', {
    channelId: props.selection.channelId,
    channelName: props.selection.channelName,
    probe: target,
    result: found || fallback
  })
}

const prevProbe = () => moveProbe(-1)
const nextProbe = () => moveProbe(1)
</script>

<style scoped>
.section-title {
  font-size: 0.8125rem;
  font-weight: 600;
  color: rgba(var(--v-theme-on-surface), 0.85);
  margin-bottom: 6px;
}

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

.verdict-footer {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  display: flex;
  align-items: center;
  gap: 12px;
}

.verdict-chip {
  min-width: 72px;
  justify-content: center;
}

.verdict-meta {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
</style>
