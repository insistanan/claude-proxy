/**
 * 评测工作台共享的展示映射。
 * 渠道行芯片、/eval 矩阵、结果抽屉共用这一份，避免各处再写一遍 verdict → 文案/颜色。
 */
import type { ApiTab, EvalProbe, EvalResult } from '@/services/api'

/** 矩阵里的一行：渠道 + 它属于哪个协议。 */
export interface EvalMatrixRow {
  channelId: string
  channelName: string
  kind: ApiTab
}

/** 点开一个格子时抽屉需要的全部上下文。 */
export interface EvalCellSelection {
  channelId: string
  channelName: string
  probe: EvalProbe
  result: EvalResult
}

/** 评测只覆盖四协议，Images 渠道不参与。 */
export const EVAL_PROTOCOL_KINDS: ApiTab[] = ['messages', 'responses', 'gemini', 'chat']

const PROTOCOL_LABELS: Record<string, string> = {
  messages: 'Messages',
  responses: 'Responses',
  gemini: 'Gemini',
  chat: 'Chat',
  images: 'Images'
}

export const evalProtocolLabel = (kind: string): string => PROTOCOL_LABELS[kind] ?? kind

const VERDICT_LABELS: Record<string, string> = {
  pass: '通过',
  suspect: '存疑',
  fail: '失败',
  error: '错误',
  insufficient: '不足',
  inapplicable: '不适用'
}

export const evalVerdictLabel = (verdict?: string): string => (verdict ? (VERDICT_LABELS[verdict] ?? verdict) : '—')

/** error / insufficient / inapplicable 一律中性灰：不是渠道判假，别报警。 */
export const evalVerdictColor = (verdict?: string): string => {
  switch (verdict) {
    case 'pass':
      return 'success'
    case 'suspect':
      return 'warning'
    case 'fail':
      return 'error'
    default:
      return 'grey'
  }
}

/** 渠道行芯片的聚合色。stale（超过 7 天）也走中性灰。 */
export const evalAggregateColor = (aggregate?: string): string => evalVerdictColor(aggregate)

const INTERVAL_LABELS: Record<string, string> = {
  '30m': '30 分钟',
  '2h': '2 小时',
  '1d': '1 天'
}

export const evalIntervalLabel = (value?: string): string => (value ? (INTERVAL_LABELS[value] ?? value) : '—')

const RUN_STATUS_LABELS: Record<string, string> = {
  queued: '排队中',
  running: '进行中',
  done: '已完成',
  partial: '部分完成',
  failed: '失败',
  cancelled: '已取消',
  skipped: '已跳过'
}

export const evalRunStatusLabel = (status?: string): string => (status ? (RUN_STATUS_LABELS[status] ?? status) : '—')

export const evalRunStatusColor = (status?: string): string => {
  switch (status) {
    case 'queued':
    case 'running':
      return 'info'
    case 'done':
      return 'success'
    case 'partial':
      return 'warning'
    case 'failed':
      return 'error'
    default:
      return 'grey'
  }
}

/**
 * 上游返回的 SVG 只在 `<img src="data:...">` 里预览。
 * data URL 中的 SVG 不执行脚本，避免用 v-html 把上游内容当本站脚本跑。
 */
export const evalSvgPreviewUrl = (svg: string): string => `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`

/** 秒级时间戳 → 本地时间；0 / 空表示还没跑过。 */
export const evalFormatTime = (unixSeconds?: number): string => {
  if (!unixSeconds) return '—'
  return new Date(unixSeconds * 1000).toLocaleString()
}

/** 批次耗时：起止秒级时间戳差，返回人读的「12 秒 / 3 分 5 秒 / 1 小时 2 分」。 */
export const evalDurationLabel = (startedAt?: number, finishedAt?: number): string => {
  if (!startedAt) return '—'
  const end = finishedAt || Math.floor(Date.now() / 1000)
  let seconds = end - startedAt
  if (seconds < 0) seconds = 0
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  const restSeconds = seconds % 60
  if (minutes < 60) return restSeconds ? `${minutes} 分 ${restSeconds} 秒` : `${minutes} 分`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes ? `${hours} 小时 ${restMinutes} 分` : `${hours} 小时`
}

/** 把一批 result 的 verdict 汇总成计数，用于批次概览与历史条目。 */
export interface EvalVerdictTally {
  pass: number
  suspect: number
  fail: number
  error: number
  insufficient: number
  inapplicable: number
  pending: number
  total: number
}

export const tallyVerdicts = (
  results: EvalResult[] | undefined,
  cellCount: number
): EvalVerdictTally => {
  const tally: EvalVerdictTally = {
    pass: 0,
    suspect: 0,
    fail: 0,
    error: 0,
    insufficient: 0,
    inapplicable: 0,
    pending: 0,
    total: cellCount
  }
  if (!results) return tally
  for (const result of results) {
    switch (result.verdict) {
      case 'pass':
        tally.pass += 1
        break
      case 'suspect':
        tally.suspect += 1
        break
      case 'fail':
        tally.fail += 1
        break
      case 'error':
        tally.error += 1
        break
      case 'insufficient':
        tally.insufficient += 1
        break
      case 'inapplicable':
        tally.inapplicable += 1
        break
      default:
        tally.pending += 1
    }
  }
  tally.pending = Math.max(0, cellCount - results.length)
  return tally
}

/** 历史卡片 mini 统计条用的分项：带颜色、标签、计数、占比。按"好→坏"排。 */
export interface TallySegment {
  key: string
  color: string
  label: string
  count: number
  ratio: number
}

const TALLY_SEGMENT_DEFS = [
  { key: 'pass', color: 'success', label: '通过' },
  { key: 'suspect', color: 'warning', label: '存疑' },
  { key: 'fail', color: 'error', label: '失败' },
  { key: 'error', color: 'grey', label: '错误' },
  { key: 'insufficient', color: 'grey', label: '不足' },
  { key: 'inapplicable', color: 'grey', label: '不适用' },
  { key: 'pending', color: 'grey', label: '待跑' }
] as const

/** 把 EvalVerdictTally 转成非零分项数组，用于批次概览条。 */
export const tallyToSegments = (tally: EvalVerdictTally | null | undefined): TallySegment[] => {
  if (!tally || tally.total === 0) return []
  return TALLY_SEGMENT_DEFS.filter(segment => tally[segment.key as keyof EvalVerdictTally] > 0).map(segment => ({
    ...segment,
    count: tally[segment.key as keyof EvalVerdictTally] as number,
    ratio: (tally[segment.key as keyof EvalVerdictTally] as number) / tally.total
  }))
}

/** 距离某个秒级时间戳还有多久，用于"下次运行"提示。 */
export const evalCountdownLabel = (unixSeconds?: number): string => {
  if (!unixSeconds) return '—'
  const seconds = unixSeconds - Math.floor(Date.now() / 1000)
  if (seconds <= 0) return '即将运行'
  if (seconds < 60) return `${seconds} 秒后`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟后`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (hours < 24) return restMinutes ? `${hours} 小时 ${restMinutes} 分后` : `${hours} 小时后`
  return `${Math.round(hours / 24)} 天后`
}

/**
 * 评测思考等级（批次级 ThinkingOverride）。
 * inherit 跟随题目 stimulus.thinking；off 关闭；enabled 兼容旧值等价 medium；
 * low/medium/high/max 按协议映射（Claude→budget_tokens，OpenAI/Responses→reasoning_effort，Gemini→thinkingBudget）。
 */
export const EVAL_THINKING_ITEMS = [
  { title: '跟随题目', value: 'inherit' },
  { title: '关闭', value: 'off' },
  { title: '低', value: 'low' },
  { title: '中', value: 'medium' },
  { title: '高', value: 'high' },
  { title: '最大', value: 'max' }
] as const

export const evalThinkingLabel = (value?: string): string =>
  EVAL_THINKING_ITEMS.find(item => item.value === value)?.title ?? value ?? '—'

/** 题目级的思考选项（stimulus.thinking），只控制"开不开"。 */
export const EVAL_STIMULUS_THINKING_ITEMS = [
  { title: '跟随批次', value: '' },
  { title: '开启思考', value: 'enabled' }
] as const
