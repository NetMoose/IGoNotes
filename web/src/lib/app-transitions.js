export async function openSettingsSafely(flush, open) {
  await flush()
  open()
}

export async function switchBaseSafely(name, flush, switchRequest, commit) {
  await flush()
  const config = await switchRequest(name)
  commit(config)
}
