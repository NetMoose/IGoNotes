export const DEFAULT_GIT_COMMIT_TEMPLATE = 'IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)'
export const GIT_INTERVALS = [5, 15, 30, 60]
export const GIT_TEMPLATE_VARIABLES = ['base', 'branch', 'date', 'datetime', 'count']

const confirmationKeys = ['create_repository', 'replace_origin', 'create_branch', 'merge_histories']
const controlCharacter = /[\u0000-\u001f\u007f]/

function trim(value) {
  return typeof value === 'string' ? value.trim() : ''
}

function localRFC3339(date) {
  const pad = (value) => String(value).padStart(2, '0')
  const offset = -date.getTimezoneOffset()
  const zone = offset === 0
    ? 'Z'
    : `${offset < 0 ? '-' : '+'}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${zone}`
}

function remoteError(value) {
  if (controlCharacter.test(value)) {
    return 'URL должен быть одной строкой'
  }
  if (!trim(value)) {
    return 'Укажите URL репозитория'
  }
  const remote = trim(value)
  if (remote.startsWith('-')) {
    return 'URL не может начинаться с дефиса'
  }
  if (!/^https?:/i.test(remote)) {
    return ''
  }
  let parsed
  try {
    parsed = new URL(remote)
  } catch {
    return 'Укажите корректный HTTP(S) URL'
  }
  if (!['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname) {
    return 'Укажите корректный HTTP(S) URL'
  }
  if (parsed.username || parsed.password) {
    return 'Не добавляйте логин или токен в URL'
  }
  if (parsed.search || parsed.hash) {
    return 'URL не должен содержать query или fragment'
  }
  return ''
}

function branchError(value) {
  if (controlCharacter.test(value)) {
    return 'Имя ветки должно быть одной строкой'
  }
  if (!trim(value)) {
    return 'Выберите ветку'
  }
  return trim(value).startsWith('-') ? 'Ветка не может начинаться с дефиса' : ''
}

function templateError(value) {
  const template = trim(value)
  if (controlCharacter.test(value)) {
    return 'Шаблон должен быть одной строкой'
  }
  if (!template) {
    return ''
  }
  if (Array.from(template).length > 200) {
    return 'Шаблон должен содержать не более 200 символов'
  }
  let remainder = template
  while (remainder) {
    const start = remainder.indexOf('{{')
    if (start < 0) {
      return remainder.includes('}}') ? 'Проверьте парные фигурные скобки' : ''
    }
    if (remainder.slice(0, start).includes('}}')) {
      return 'Проверьте парные фигурные скобки'
    }
    const end = remainder.indexOf('}}', start + 2)
    if (end < 0) {
      return 'Проверьте парные фигурные скобки'
    }
    const token = remainder.slice(start + 2, end)
    if (!GIT_TEMPLATE_VARIABLES.includes(token)) {
      return `Неизвестная переменная {{${token}}}`
    }
    remainder = remainder.slice(end + 2)
  }
  return ''
}

function normalizedDraft(draft) {
  return {
    gitURL: trim(draft?.gitURL),
    branch: trim(draft?.branch),
    autoSync: draft?.autoSync === true,
    interval: draft?.interval,
    template: trim(draft?.template) || DEFAULT_GIT_COMMIT_TEMPLATE,
  }
}

export function gitConfigured(base) {
  return Boolean(trim(base?.git_url) && trim(base?.git_branch))
}

export function validateGitDraft(draft) {
  const errors = {}
  const remote = remoteError(draft?.gitURL)
  const branch = branchError(draft?.branch)
  const template = templateError(draft?.template)
  if (remote) errors.gitURL = remote
  if (branch) errors.branch = branch
  if (draft?.autoSync === true && !GIT_INTERVALS.includes(draft?.interval)) {
    errors.interval = 'Выберите интервал 5, 15, 30 или 60 минут'
  }
  if (template) errors.template = template
  return errors
}

export function renderGitCommitPreview(template, { base = '', branch = '', count = 3, at = new Date() } = {}) {
  const datetime = localRFC3339(at)
  return (trim(template) || DEFAULT_GIT_COMMIT_TEMPLATE)
    .replaceAll('{{base}}', base)
    .replaceAll('{{branch}}', branch)
    .replaceAll('{{date}}', datetime.slice(0, 10))
    .replaceAll('{{datetime}}', datetime)
    .replaceAll('{{count}}', String(count))
}

export function requiredConfirmationKeys(probe) {
  return confirmationKeys.filter((key) => probe?.required_mutations?.[key] === true)
}

export function buildGitConfigRequest(draft, probe, checks = {}) {
  const value = normalizedDraft(draft)
  const required = new Set(requiredConfirmationKeys(probe))
  return {
    git_url: value.gitURL,
    git_branch: value.branch,
    auto_sync: value.autoSync,
    auto_sync_interval_minutes: GIT_INTERVALS.includes(value.interval) ? value.interval : 15,
    git_commit_message_template: value.template,
    confirmations: Object.fromEntries(confirmationKeys.map((key) => [key, required.has(key) && checks[key] === true])),
  }
}

export function replaceConfigBase(config, base) {
  return {
    ...config,
    bases: Array.isArray(config?.bases)
      ? config.bases.map((item) => item?.name === base?.name ? base : item)
      : [],
  }
}

export function gitStatusFor(statuses, base) {
  return Array.isArray(statuses) ? statuses.find((status) => status?.base === base) ?? null : null
}

export function presentGitStatus(status) {
  switch (status?.state) {
    case 'initializing':
    case 'syncing':
      return { label: 'Выполняется', tone: 'blue', busy: true, canSync: false }
    case 'ready':
      return status.ahead > 0
        ? { label: 'Есть локальные изменения', tone: 'amber', busy: false, canSync: true }
        : { label: 'Синхронизировано', tone: 'green', busy: false, canSync: true }
    case 'error':
      return { label: 'Ошибка', tone: 'red', busy: false, canSync: true }
    case 'paused':
      return { label: 'Приостановлено', tone: 'amber', busy: false, canSync: false }
    case 'conflict':
      return { label: 'Конфликт', tone: 'red', busy: false, canSync: false }
    case 'needs_reconnect':
      return { label: 'Требуется переподключение', tone: 'amber', busy: false, canSync: false }
    case 'unconfigured':
    default:
      return status?.state && status.state !== 'unconfigured'
        ? { label: 'Статус неизвестен', tone: 'slate', busy: false, canSync: false }
        : { label: 'Git не настроен', tone: 'slate', busy: false, canSync: false }
  }
}
