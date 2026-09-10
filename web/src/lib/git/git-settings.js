export const DEFAULT_GIT_COMMIT_TEMPLATE = 'IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)'
export const GIT_INTERVALS = [5, 15, 30, 60]
export const GIT_TEMPLATE_VARIABLES = ['base', 'branch', 'date', 'datetime', 'count']

const confirmationKeys = ['create_repository', 'replace_origin', 'create_branch', 'merge_histories']

function string(value) {
  return typeof value === 'string' ? value.trim() : ''
}

function validGitURL(value) {
  if (!value || value.startsWith('-') || /[\u0000-\u001f\u007f]/.test(value)) {
    return false
  }
  if (value.includes('://')) {
    let parsed
    try {
      parsed = new URL(value)
    } catch {
      return false
    }
    if (parsed.search || parsed.hash) {
      return false
    }
    if (['http:', 'https:'].includes(parsed.protocol)) {
      return Boolean(parsed.hostname) && !parsed.username && !parsed.password
    }
    if (parsed.protocol === 'ssh:') {
      return Boolean(parsed.hostname) && !parsed.password
    }
    if (parsed.protocol === 'git:') {
      return Boolean(parsed.hostname) && !parsed.username && !parsed.password
    }
    return parsed.protocol === 'file:' && !parsed.username && Boolean(parsed.pathname)
  }
  if (/^(?:http|https|ssh|git|file):/i.test(value) || value.includes('::')) {
    return false
  }
  if (!value.includes(':')) {
    return true
  }
  const colon = value.indexOf(':')
  const separator = value.search(/[\\/]/)
  if (separator >= 0 && separator < colon) {
    return true
  }
  const match = /^(?:[^:@/\\\[\]\s]+@)?([^:/\\\[\]\s]+):(\S+)$/.exec(value)
  return match !== null
}

function validBranch(value) {
  const components = value.split('/')
  return Boolean(value)
    && !/[\u0000-\u001f\u007f ~^:?*\[\\]/.test(value)
    && value !== '@'
    && !value.includes('..')
    && !value.includes('@{')
    && components.every((component) => component
      && !component.startsWith('.')
      && !component.endsWith('.')
      && !component.endsWith('.lock'))
}

function validTemplate(value) {
  if (!value || value.length > 200 || /[\u0000-\u001f\u007f]/.test(value)) {
    return false
  }
  let remainder = value
  while (remainder) {
    const start = remainder.indexOf('{{')
    if (start < 0) {
      return !remainder.includes('}}')
    }
    if (remainder.slice(0, start).includes('}}')) {
      return false
    }
    const end = remainder.indexOf('}}', start + 2)
    if (end < 0 || !GIT_TEMPLATE_VARIABLES.includes(remainder.slice(start + 2, end))) {
      return false
    }
    remainder = remainder.slice(end + 2)
  }
  return true
}

function localRFC3339(date) {
  const pad = (value) => String(value).padStart(2, '0')
  const offset = -date.getTimezoneOffset()
  const zone = offset === 0
    ? 'Z'
    : `${offset < 0 ? '-' : '+'}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${zone}`
}

function normalizedDraft(draft) {
  return {
    git_url: string(draft?.git_url),
    git_branch: string(draft?.git_branch),
    auto_sync: draft?.auto_sync === true,
    auto_sync_interval_minutes: draft?.auto_sync_interval_minutes,
    git_commit_message_template: string(draft?.git_commit_message_template),
  }
}

export function gitConfigured(base) {
  return Boolean(string(base?.git_url) && string(base?.git_branch))
}

export function validateGitDraft(draft) {
  const value = normalizedDraft(draft)
  const errors = {}
  if (!value.git_url) {
    errors.git_url = 'Укажите URL Git-репозитория'
  } else if (!validGitURL(value.git_url)) {
    errors.git_url = 'Укажите корректный URL Git-репозитория'
  }
  if (!value.git_branch) {
    errors.git_branch = 'Укажите ветку Git'
  } else if (!validBranch(value.git_branch)) {
    errors.git_branch = 'Укажите корректное имя ветки Git'
  }
  if ((value.auto_sync || value.auto_sync_interval_minutes !== 0) && !GIT_INTERVALS.includes(value.auto_sync_interval_minutes)) {
    errors.auto_sync_interval_minutes = 'Выберите интервал автосинхронизации'
  }
  if (!value.git_commit_message_template) {
    errors.git_commit_message_template = 'Введите шаблон сообщения коммита'
  } else if (!validTemplate(value.git_commit_message_template)) {
    errors.git_commit_message_template = 'Шаблон содержит неподдерживаемую переменную'
  }
  return errors
}

export function renderGitCommitPreview(template, { base = '', branch = '', date = new Date(), count = 0 } = {}) {
  const value = string(template) || DEFAULT_GIT_COMMIT_TEMPLATE
  const datetime = localRFC3339(date)
  return value
    .replaceAll('{{base}}', base)
    .replaceAll('{{branch}}', branch)
    .replaceAll('{{date}}', datetime.slice(0, 10))
    .replaceAll('{{datetime}}', datetime)
    .replaceAll('{{count}}', String(count))
}

export function requiredConfirmationKeys(probe) {
  const mutations = probe?.required_mutations
  return confirmationKeys.filter((key) => mutations?.[key] === true)
}

export function buildGitConfigRequest(draft, probe, checks = {}) {
  const value = normalizedDraft(draft)
  const required = new Set(requiredConfirmationKeys(probe))
  return {
    ...value,
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
