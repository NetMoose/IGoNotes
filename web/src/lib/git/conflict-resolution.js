function nonemptyString(value) {
  return typeof value === 'string' && value.trim().length > 0
}

function required(value, field) {
  if (!nonemptyString(value)) {
    throw new Error(`Необходимо поле ${field}`)
  }
  return value
}

export function suggestSidePath(path, side) {
  const slash = path.lastIndexOf('/')
  const dot = path.lastIndexOf('.')
  const suffix = `-${side}`
  if (dot > slash + 1) {
    return `${path.slice(0, dot)}${suffix}${path.slice(dot)}`
  }
  return `${path}${suffix}`
}

export function buildResolution({ base, operationId }, conflict, draft) {
  const action = draft?.action
  if (!Array.isArray(conflict?.actions) || !conflict.actions.includes(action)) {
    throw new Error('Недоступное действие')
  }

  const resolution = {
    base: required(base, 'base'),
    operation_id: required(operationId, 'operation_id'),
    conflict_id: required(conflict.id, 'conflict_id'),
    path: required(conflict.path, 'path'),
    action,
  }

  if (action === 'local' || action === 'remote') {
    resolution.result_path = required(draft.resultPath, 'result_path')
    resolution[`${action}_oid`] = required(conflict[action]?.oid, `${action}_oid`)
  } else if (action === 'manual') {
    resolution.result_path = required(draft.resultPath, 'result_path')
    if (typeof draft.content !== 'string') {
      throw new Error('Необходимо поле content')
    }
    resolution.content = draft.content
  } else if (action === 'keep_both') {
    resolution.local_path = required(draft.localPath, 'local_path')
    resolution.remote_path = required(draft.remotePath, 'remote_path')
    if (resolution.local_path === resolution.remote_path) {
      throw new Error('Поля local_path и remote_path должны различаться')
    }
    resolution.local_oid = required(conflict.local?.oid, 'local_oid')
    resolution.remote_oid = required(conflict.remote?.oid, 'remote_oid')
  }

  return resolution
}
