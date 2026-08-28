import { readFile } from 'node:fs/promises'
import { describe, expect, it } from 'vitest'

const testModuleUrl = import.meta.url

describe('PWA metadata', () => {
  it('defines the installable web app manifest', async () => {
    const contents = await readFile(new URL('../public/manifest.webmanifest', testModuleUrl), 'utf8')
    const manifest = JSON.parse(contents)

    expect(manifest).toEqual({
      id: '/',
      name: 'IGoNotes',
      short_name: 'IGoNotes',
      start_url: '/',
      scope: '/',
      display: 'standalone',
      theme_color: '#2563eb',
      background_color: '#f1f5f9',
      prefer_related_applications: false,
      icons: [
        { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
        { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
        { src: '/icons/icon-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
      ],
    })
  })

  it('links the manifest and browser metadata from the HTML shell', async () => {
    const contents = await readFile(new URL('../index.html', testModuleUrl), 'utf8')
    const document = new DOMParser().parseFromString(contents, 'text/html')

    expect(document.documentElement.lang).toBe('ru')
    expect(document.querySelector('link[rel="manifest"]')?.getAttribute('href')).toBe('/manifest.webmanifest')
    expect(document.querySelector('link[rel="icon"]')?.getAttribute('href')).toBe('/favicon.svg')
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#2563eb')
  })
})
