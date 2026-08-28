export function normalizeBaseDraft(draft) {
  return {
    mode: typeof draft?.mode === 'string' ? draft.mode : '',
    name: typeof draft?.name === 'string' ? draft.name.trim() : '',
    path: typeof draft?.path === 'string' ? draft.path.trim() : '',
  }
}

export function validateBaseDraft(draft, { existingNames = [], originalName = '' } = {}) {
  const normalized = normalizeBaseDraft(draft)
  const errors = {}

  if (!normalized.name) {
    errors.name = 'Введите имя базы'
  } else if (
    normalized.mode === 'create'
    && (normalized.name === '.' || normalized.name === '..' || /[\\/]/.test(normalized.name))
  ) {
    errors.name = 'Имя новой базы не может быть точкой и содержать / или \\'
  } else if (
    normalized.name !== originalName
    && Array.isArray(existingNames)
    && existingNames.includes(normalized.name)
  ) {
    errors.name = 'База с таким именем уже добавлена'
  }

  if (!normalized.path) {
    errors.path = 'Укажите каталог'
  }

  return errors
}

export function resolveBasePath(draft) {
  const normalized = normalizeBaseDraft(draft)
  if (normalized.mode !== 'create') {
    return normalized.path
  }

  const separator = normalized.path.includes('\\') && !normalized.path.includes('/') ? '\\' : '/'
  return `${normalized.path.replace(/[\\/]+$/, '')}${separator}${normalized.name}`
}

export function activeBase(config) {
  if (!config || typeof config.current_base !== 'string' || !Array.isArray(config.bases)) {
    return null
  }

  return config.bases.find((base) => base?.name === config.current_base) ?? null
}
