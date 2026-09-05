// 应用设置与内容安全类型。

export interface AppSettings {
  network: {
    upstreamProxyUrl: string
    upstreamProxyEnabled: boolean
  }
  contentSafety: ContentSafetySettings
  integration: {
    /** CC Switch 可执行文件路径；空串表示未配置 */
    ccSwitchPath: string
  }
}

export interface ContentSafetySettings {
  sensitiveWord: {
    enabled: boolean
    pornographyEnabled: boolean
    gamblingEnabled: boolean
    drugsEnabled: boolean
    violenceTerrorEnabled: boolean
    politicalEnabled: boolean
    illegalCrimeEnabled: boolean
    customWords: string[]
  }
  sensitiveInfo: {
    enabled: boolean
    mode: ContentSafetyMode
    enabledRules: SensitiveInfoRule[]
    ipMaskScope: SensitiveInfoIPMaskScope
  }
  credential: {
    enabled: boolean
    userInputMode: ContentSafetyMode
    toolResultMode: Exclude<ContentSafetyMode, 'mask'>
    toolArgumentMode: Exclude<ContentSafetyMode, 'mask'>
    enabledRules: CredentialRule[]
  }
  dangerousCmd: {
    enabled: boolean
    enabledRules: DangerousCommandRule[]
  }
  whitelist: {
    enabled: boolean
    toolNames: string[]
  }
}

export type ContentSafetyMode = 'audit' | 'block' | 'mask'
export type SensitiveInfoRule = 'phone' | 'id_card' | 'email' | 'ip_address'
export type SensitiveInfoIPMaskScope = 'public' | 'all'
export type CredentialRule = 'api_key' | 'named_secret' | 'private_key' | 'connection_string' | 'high_entropy'
export type DangerousCommandRule =
  | 'destructive'
  | 'download_execute'
  | 'reverse_shell'
  | 'privilege_escalation'
  | 'environment_tampering'
