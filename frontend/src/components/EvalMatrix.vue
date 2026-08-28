<template>
  <div v-if="!run || !groups.length" class="text-body-2 text-medium-emphasis py-4">
    没有可显示的结果
  </div>

  <div v-else class="matrix-groups">
    <section v-for="group in groups" :key="group.kind">
      <div class="d-flex align-center ga-2 mb-2">
        <div class="text-body-2 font-weight-medium">{{ evalProtocolLabel(group.kind) }}</div>
        <v-chip size="x-small" variant="tonal">{{ group.rows.length }}</v-chip>
      </div>

      <div class="matrix-wrap">
        <table class="matrix-table">
          <thead>
            <tr>
              <th class="matrix-head-channel">渠道</th>
              <th v-for="probe in probes" :key="probe.id">
                <div class="text-truncate">{{ probe.name }}</div>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in group.rows" :key="row.channelId">
              <td class="matrix-head-channel">
                <div class="text-truncate">{{ row.channelName }}</div>
              </td>
              <td v-for="probe in probes" :key="probe.id">
                <v-chip
                  size="small"
                  variant="tonal"
                  :color="cellColor(row.channelId, probe.id)"
                  :class="{ 'cell-clickable': !!cellOf(row.channelId, probe.id) }"
                  @click="openCell(row, probe)"
                >
                  {{ cellLabel(row.channelId, probe.id) }}
                </v-chip>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ApiTab, Channel, EvalProbe, EvalResult, EvalRun } from '@/services/api'
import type { EvalCellSelection, EvalMatrixRow } from '@/utils/eval'
import { EVAL_PROTOCOL_KINDS, evalProtocolLabel, evalVerdictColor, evalVerdictLabel } from '@/utils/eval'

const props = defineProps<{
  run: EvalRun | null
  /** 矩阵的列：本次套件里的探针，按套件顺序 */
  probes: EvalProbe[]
  channelsByKind: Record<ApiTab, Channel[]>
}>()

const emit = defineEmits<{
  cell: [EvalCellSelection]
}>()

const locate = (channelId: string): EvalMatrixRow | null => {
  for (const kind of EVAL_PROTOCOL_KINDS) {
    const found = (props.channelsByKind[kind] || []).find(channel => channel.id === channelId)
    if (found) return { channelId, channelName: found.name, kind }
  }
  return null
}

/** 按协议分组行，40 条渠道混一张表没法看。批次里已被删掉的渠道直接不画。 */
const groups = computed(() => {
  const map = new Map<ApiTab, { kind: ApiTab; rows: EvalMatrixRow[] }>()
  for (const channelId of props.run?.channelIds || []) {
    const row = locate(channelId)
    if (!row) continue
    const group = map.get(row.kind) || { kind: row.kind, rows: [] }
    group.rows.push(row)
    map.set(row.kind, group)
  }
  return EVAL_PROTOCOL_KINDS.map(kind => map.get(kind)).filter((group): group is { kind: ApiTab; rows: EvalMatrixRow[] } => !!group)
})

const cellOf = (channelId: string, probeId: string): EvalResult | undefined =>
  props.run?.results?.find(result => result.channelId === channelId && result.probeId === probeId)

const cellLabel = (channelId: string, probeId: string) => {
  const result = cellOf(channelId, probeId)
  if (!result) return '待跑'
  return evalVerdictLabel(result.verdict)
}

const cellColor = (channelId: string, probeId: string) => {
  const result = cellOf(channelId, probeId)
  if (!result) return 'grey'
  return evalVerdictColor(result.verdict)
}

const openCell = (row: EvalMatrixRow, probe: EvalProbe) => {
  const result = cellOf(row.channelId, probe.id)
  if (!result) return
  emit('cell', { channelId: row.channelId, channelName: row.channelName, probe, result })
}
</script>

<style scoped>
.matrix-groups {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.matrix-wrap {
  overflow-x: auto;
}

.matrix-table {
  border-collapse: collapse;
  width: 100%;
}

.matrix-table th,
.matrix-table td {
  padding: 6px 10px;
  text-align: left;
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.12);
  white-space: nowrap;
  vertical-align: middle;
}

.matrix-table th {
  font-weight: 500;
  font-size: 0.8125rem;
}

.matrix-head-channel {
  max-width: 220px;
  position: sticky;
  left: 0;
  background: rgb(var(--v-theme-surface));
}

.cell-clickable {
  cursor: pointer;
}
</style>
