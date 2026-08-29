// 评测工作台类型：题库、套件、批次与值班配置。

export interface EvalStimulus {
  prompt: string
  system?: string
  maxTokens?: number
  temperature?: number
  thinking?: string
  thinkingBudget?: number
  forceJson?: boolean
}

export interface EvalExtractSpec {
  kind: string
}

export interface EvalJudgeSpec {
  kind: string
  expected?: string
  expectedNumber?: number
  pattern?: string
  caseInsensitive?: boolean
  expectModelEcho?: boolean
  expectContentTypes?: string[]
  expectUsage?: boolean
  expectThinking?: string
  expectSignature?: string
  minSamples?: number
  failIfIdentical?: boolean
  suspectUniqueRatio?: number
  compareHistogram?: boolean
  analysisPrompt?: string
  expectThinkingUsage?: boolean
}

export interface EvalProbe {
  id: string
  slug: string
  name: string
  /** 题目用途说明，界面直接展示 */
  description?: string
  category: 'authenticity' | 'iq' | string
  stimulus: EvalStimulus
  extract: EvalExtractSpec
  judge: EvalJudgeSpec
  sampleCount: number
  applicableServiceTypes: string[]
  cheap: boolean
  /** 内置题由 seed.go 维护，每次启动按代码同步，改不了删不了 */
  builtin: boolean
  createdAt: number
  updatedAt: number
}

export interface EvalSuite {
  id: string
  slug: string
  name: string
  /** 套件用途说明，界面直接展示 */
  description?: string
  probeIds: string[]
  cheap: boolean
  builtin: boolean
  createdAt: number
  updatedAt: number
}

export interface EvalResult {
  id: string
  runId: string
  channelId: string
  probeId: string
  probeName?: string
  verdict: string
  excerpt?: string
  detail?: Record<string, unknown>
  latencyMs: number
  createdAt: number
}

export interface EvalRun {
  id: string
  suiteId: string
  suiteName?: string
  trigger: string
  status: string
  channelIds: string[]
  model?: string
  /** channelId → 渠道专属模型覆盖，优先于统一 model */
  channelModels?: Record<string, string>
  thinking?: string
  /** channelId → 渠道专属思考等级覆盖（off/low/medium/high/max），优先于统一 thinking */
  channelThinking?: Record<string, string>
  estimatedCalls: number
  skipReason?: string
  error?: string
  startedAt: number
  finishedAt: number
  createdAt: number
  results?: EvalResult[]
  /** 批次 verdict 聚合计数。ListRuns 带它，历史卡片直接画 mini 条不用再逐条拉 results。 */
  tally?: EvalRunTally
}

/** 后端 RunTally 对应：7 个 verdict 计数 + total。 */
export interface EvalRunTally {
  pass: number
  suspect: number
  fail: number
  error: number
  insufficient: number
  inapplicable: number
  total: number
}

export interface EvalWatchConfig {
  enabled: boolean
  suiteId: string
  interval: string
  channelIds: string[]
  model?: string
  /** channelId → 渠道专属模型覆盖，优先于统一 model */
  channelModels?: Record<string, string>
  thinking?: string
  /** channelId → 渠道专属思考等级覆盖，优先于统一 thinking */
  channelThinking?: Record<string, string>
  lastRunAt: number
  nextRunAt: number
  lastSkipReason?: string
}

export interface EvalChannelLatest {
  channelId: string
  aggregate: string
  label: string
  suiteName?: string
  finishedAt: number
  watching: boolean
  runId?: string
}

export interface EvalStartRunRequest {
  suiteId: string
  channelIds: string[]
  model?: string
  /** channelId → 渠道专属模型覆盖，优先于统一 model */
  channelModels?: Record<string, string>
  thinking?: string
  /** channelId → 渠道专属思考等级覆盖，优先于统一 thinking */
  channelThinking?: Record<string, string>
  /** 非空时只跑这里列出的探针，空则跑套件全部 */
  probeIds?: string[]
  trigger?: string
}

export interface EvalValidateReport {
  ok: boolean
  mode: string
  errors?: string[]
  extractKind?: string
  judgeKind?: string
  cheap: boolean
}
