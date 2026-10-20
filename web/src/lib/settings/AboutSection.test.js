import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => ({
  ...await importOriginal(),
  getInfo: vi.fn(),
}))

import { getInfo } from '../api.js'
import SettingsWorkspace from './SettingsWorkspace.svelte'

async function openAbout() {
  render(SettingsWorkspace, {
    config: { bases: [], current_base: '', setup_completed: true },
    onConfigChange: vi.fn(),
    onSwitch: vi.fn(),
    onBack: vi.fn(),
  })
  expect(getInfo).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('tab', { name: 'О программе' }))
  return within(screen.getByRole('tabpanel', { name: 'О программе' }))
}

describe('AboutSection through settings', () => {
  beforeEach(() => {
    vi.mocked(getInfo).mockReset()
  })

  it('loads the running application name and exact release version', async () => {
    let resolve
    vi.mocked(getInfo).mockReturnValue(new Promise((done) => { resolve = done }))
    const panel = await openAbout()
    expect(panel.getByRole('status')).toHaveTextContent('Загрузка сведений о программе')
    expect(panel.queryByText('Сборка разработки')).not.toBeInTheDocument()

    resolve({ base_path: '/notes', app_name: 'IGoNotes', app_version: 'v0.0.1-alpha.99' })

    expect(await panel.findByText('v0.0.1-alpha.99')).toBeVisible()
    expect(panel.getByText('IGoNotes')).toBeVisible()
    expect(panel.getByText('Версия')).toBeVisible()
    expect(panel.queryByRole('status')).not.toBeInTheDocument()
  })

  it('labels an explicit dev version as a development build', async () => {
    vi.mocked(getInfo).mockResolvedValue({ app_name: 'IGoNotes', app_version: 'dev' })
    const panel = await openAbout()
    expect(await panel.findByText('Сборка разработки')).toBeVisible()
  })

  it('retries a failed request without pretending it is a development build', async () => {
    vi.mocked(getInfo)
      .mockRejectedValueOnce(new Error('Сервер недоступен'))
      .mockResolvedValueOnce({ app_name: 'IGoNotes', app_version: 'v0.0.1-alpha.99' })
    const panel = await openAbout()
    expect(await panel.findByRole('alert')).toHaveTextContent('Сервер недоступен')
    expect(panel.queryByText('Сборка разработки')).not.toBeInTheDocument()

    await userEvent.setup().click(panel.getByRole('button', { name: 'Повторить загрузку' }))

    expect(await panel.findByText('v0.0.1-alpha.99')).toBeVisible()
    expect(panel.queryByRole('alert')).not.toBeInTheDocument()
    expect(getInfo).toHaveBeenCalledTimes(2)
  })

  it.each([
    null,
    {},
    { app_name: 'IGoNotes' },
    { app_name: 'IGoNotes', app_version: ' ' },
    { app_name: 'IGoNotes', app_version: 5 },
    { app_name: '', app_version: 'dev' },
  ])('reports malformed metadata instead of a development version: %j', async (info) => {
    vi.mocked(getInfo).mockResolvedValue(info)
    const panel = await openAbout()
    expect(await panel.findByRole('alert')).toHaveTextContent('Некорректные сведения о программе')
    expect(panel.queryByText('Сборка разработки')).not.toBeInTheDocument()
  })
})
