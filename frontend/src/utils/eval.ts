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
