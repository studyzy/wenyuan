/**
 * Locale copy for the WenYuan memory card.
 * @module @studyzy/dsh-wenyuan-memory/settings-locales
 */

/** Locale keys the card renders. */
export type WenYuanMemoryLocaleKey =
  | 'title'
  | 'description'
  | 'apiUrl'
  | 'apiUrlHint'
  | 'bankId'
  | 'bankIdHint'
  | 'apiKey'
  | 'apiKeyHint'
  | 'apiKeySet'
  | 'apiKeyUnset'
  | 'injectContext'
  | 'injectContextHint'
  | 'retainOnStop'
  | 'retainOnStopHint'
  | 'overridden'
  | 'reset'
  | 'invalidUrl'
  | 'unavailable'
  | 'readOnly'
  | 'save'
  | 'saving'
  | 'saveFailed'

declare module '@deepseek-ai/dsh-client-ui-slots' {
  interface LocaleNamespaceMap {
    'wenyuan-memory.settings': WenYuanMemoryLocaleKey
  }
}

/** English copy for the card. */
export const en: Record<WenYuanMemoryLocaleKey, string> = {
  title: 'WenYuan memory',
  description: 'Long-term project memory served by a WenYuan server',
  apiUrl: 'Server URL',
  apiUrlHint: 'Base URL of the WenYuan service, for example http://127.0.0.1:18888.',
  bankId: 'Memory bank',
  bankIdHint:
    'Leave blank (or set auto) to derive the bank from the current project: coding-agent::<repo>, '
    + 'taken from the working directory\'s git remote, or its directory name when there is no git remote. '
    + 'A session with no working directory uses coding-agent::workspace. Type a name to share one bank across projects.',
  apiKey: 'API key',
  apiKeyHint: 'Stored as WENYUAN_API_KEY. Leave blank to keep the current key.',
  apiKeySet: 'A key is configured.',
  apiKeyUnset: 'No key configured; the server must not require one.',
  injectContext: 'Attach memory to prompts',
  injectContextHint: 'Recall relevant memory at session start and before the first prompt of a turn.',
  retainOnStop: 'Write sessions back',
  retainOnStopHint: 'Hand the finished turn to the server so later sessions can recall it.',
  overridden: 'overridden',
  reset: 'Reset',
  invalidUrl: 'Enter an http:// or https:// URL.',
  unavailable: 'This server does not expose the WenYuan plugin settings.',
  readOnly: 'Settings are read-only in this deployment.',
  save: 'Save',
  saving: 'Saving…',
  saveFailed: 'Save failed; the server rejected the values.',
}

/** Simplified Chinese copy for the card. */
export const zh: Record<WenYuanMemoryLocaleKey, string> = {
  title: '文渊记忆',
  description: '由文渊服务提供的长期项目记忆',
  apiUrl: '服务地址',
  apiUrlHint: '文渊服务的基础地址，例如 http://127.0.0.1:18888。',
  bankId: '记忆库',
  bankIdHint:
    '留空（或填 auto）= 按当前项目自动推导（coding-agent::<repo>）：取工作目录 git remote 的仓库名，'
    + '没有 remote 就用目录名；无工作目录时回退 coding-agent::workspace。'
    + '填具体名字则所有项目共用一个记忆库。',
  apiKey: 'API Key',
  apiKeyHint: '保存为 WENYUAN_API_KEY；留空则保留当前 Key。',
  apiKeySet: '已配置 Key。',
  apiKeyUnset: '未配置 Key，服务端不应要求鉴权。',
  injectContext: '自动注入记忆',
  injectContextHint: '在会话开始和每轮首个提示词前召回相关记忆。',
  retainOnStop: '会话回写',
  retainOnStopHint: '在一轮结束时把对话交给服务端，供后续会话召回。',
  overridden: '已覆盖',
  reset: '重置',
  invalidUrl: '请输入 http:// 或 https:// 开头的地址。',
  unavailable: '当前服务端未提供文渊插件配置。',
  readOnly: '当前部署下设置为只读。',
  save: '保存',
  saving: '保存中…',
  saveFailed: '保存失败：服务端拒绝了该值。',
}