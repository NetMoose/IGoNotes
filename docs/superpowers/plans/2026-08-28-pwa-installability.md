# PWA Installability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the local IGoNotes page installable from desktop Chromium with a branded `IG` icon and standalone display mode.

**Architecture:** Keep the existing Svelte/Vite and embedded Go server architecture. Add static PWA metadata and pre-rendered icons under `web/public`, let Vite copy them into `web/dist`, and make the existing SPA handler return the required manifest MIME type without adding a service worker or runtime frontend logic.

**Tech Stack:** Svelte 5, Vite 8, Vitest 4, Web App Manifest, SVG/PNG, ImageMagick, Go 1.26 `net/http`

---

## Execution Prerequisite

Run every npm, Vitest, Vite, and `make` command in this plan under Node 24.20.0. The locked frontend dependencies require a supported Node 22+/24 runtime; this plan standardizes verification on Node 24.20.0.

Activate and verify the runtime before executing the plan:

```bash
nvm use 24.20.0
node --version
```

Expected `node --version` output: `v24.20.0`.

Do not add an `.nvmrc` or change package metadata as part of this work. This prerequisite documents the verified execution environment rather than changing project-wide toolchain policy.

## File Structure

- Create `web/src/pwa-metadata.test.js`: verify the manifest contract and HTML metadata.
- Create `web/src/pwa-icons.test.js`: verify PNG chunk structure and decoded image data, declared dimensions and formats, and maskable opacity.
- Create `web/public/manifest.webmanifest`: describe the installable application and its icons.
- Modify `web/index.html`: connect the page to the manifest, branded favicon, language, and theme color.
- Replace `web/public/favicon.svg`: store the approved blue `IG` monogram as the source icon and browser favicon.
- Create `web/public/icons/icon-192.png`: standard 192px install icon.
- Create `web/public/icons/icon-512.png`: standard 512px install icon.
- Create `web/public/icons/icon-maskable-512.png`: full-bleed maskable 512px install icon.
- Create `internal/handlers/static_handler_test.go`: verify static PWA assets and their content types.
- Modify `internal/handlers/static_handler.go`: set the manifest MIME type before delegating to `http.FileServer`.

No service worker, install button, API behavior, note storage, or application routing changes are included.

### Task 1: Add The Manifest And Page Metadata

**Files:**
- Create: `web/src/pwa-metadata.test.js`
- Create: `web/public/manifest.webmanifest`
- Modify: `web/index.html:2-8`

- [ ] **Step 1: Write the failing PWA metadata test**

Create `web/src/pwa-metadata.test.js`:

```js
import { readFile } from 'node:fs/promises'
import { describe, expect, it } from 'vitest'

describe('PWA metadata', () => {
  it('defines the Chromium installability manifest contract', async () => {
    const source = await readFile(new URL('../public/manifest.webmanifest', import.meta.url), 'utf8')
    const manifest = JSON.parse(source)

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
        {
          src: '/icons/icon-192.png',
          sizes: '192x192',
          type: 'image/png',
          purpose: 'any',
        },
        {
          src: '/icons/icon-512.png',
          sizes: '512x512',
          type: 'image/png',
          purpose: 'any',
        },
        {
          src: '/icons/icon-maskable-512.png',
          sizes: '512x512',
          type: 'image/png',
          purpose: 'maskable',
        },
      ],
    })
  })

  it('links the manifest and branded browser metadata', async () => {
    const html = await readFile(new URL('../index.html', import.meta.url), 'utf8')
    const page = new DOMParser().parseFromString(html, 'text/html')

    expect(page.documentElement.lang).toBe('ru')
    expect(page.querySelector('link[rel="manifest"]')?.getAttribute('href')).toBe('/manifest.webmanifest')
    expect(page.querySelector('link[rel="icon"]')?.getAttribute('href')).toBe('/favicon.svg')
    expect(page.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#2563eb')
  })
})
```

- [ ] **Step 2: Run the metadata test and verify it fails**

Run:

```bash
npm test -- --run src/pwa-metadata.test.js
```

Working directory: `web`

Expected: FAIL with `ENOENT` for `public/manifest.webmanifest`.

- [ ] **Step 3: Add the web app manifest**

Create `web/public/manifest.webmanifest`:

```json
{
  "id": "/",
  "name": "IGoNotes",
  "short_name": "IGoNotes",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "theme_color": "#2563eb",
  "background_color": "#f1f5f9",
  "prefer_related_applications": false,
  "icons": [
    {
      "src": "/icons/icon-192.png",
      "sizes": "192x192",
      "type": "image/png",
      "purpose": "any"
    },
    {
      "src": "/icons/icon-512.png",
      "sizes": "512x512",
      "type": "image/png",
      "purpose": "any"
    },
    {
      "src": "/icons/icon-maskable-512.png",
      "sizes": "512x512",
      "type": "image/png",
      "purpose": "maskable"
    }
  ]
}
```

- [ ] **Step 4: Link the manifest and page metadata**

Change the beginning of `web/index.html` to:

```html
<!doctype html>
<html lang="ru">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
    <link rel="manifest" href="/manifest.webmanifest" />
    <meta name="theme-color" content="#2563eb" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>IGoNotes</title>
  </head>
```

Leave the existing `<body>` unchanged.

- [ ] **Step 5: Re-run the metadata test**

Run:

```bash
npm test -- --run src/pwa-metadata.test.js
```

Working directory: `web`

Expected: PASS, 2 tests passed.

### Task 2: Add The Branded Install Icons

**Files:**
- Create: `web/src/pwa-icons.test.js`
- Replace: `web/public/favicon.svg`
- Create: `web/public/icons/icon-192.png`
- Create: `web/public/icons/icon-512.png`
- Create: `web/public/icons/icon-maskable-512.png`

- [ ] **Step 1: Write the failing icon validation test**

Create `web/src/pwa-icons.test.js`:

```js
// @vitest-environment node

import { readFile } from 'node:fs/promises'
import { inflateSync } from 'node:zlib'
import { describe, expect, it } from 'vitest'

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
const testModuleUrl = import.meta.url

function crc32(contents) {
  let crc = 0xffffffff

  for (const byte of contents) {
    crc ^= byte
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1))
    }
  }

  return (crc ^ 0xffffffff) >>> 0
}

function requirePng(condition, message) {
  if (!condition) throw new Error(`Invalid PNG: ${message}`)
}

function parsePng(contents) {
  requirePng(contents.subarray(0, 8).equals(PNG_SIGNATURE), 'bad signature')

  let offset = 8
  let ihdr
  const chunkTypes = new Set()
  const idatPayloads = []

  while (offset < contents.length) {
    requirePng(offset + 12 <= contents.length, 'truncated chunk')

    const length = contents.readUInt32BE(offset)
    const dataStart = offset + 8
    const crcOffset = dataStart + length
    const chunkEnd = crcOffset + 4
    requirePng(chunkEnd <= contents.length, 'chunk exceeds file length')

    const type = contents.toString('ascii', offset + 4, dataStart)
    const expectedCrc = contents.readUInt32BE(crcOffset)
    requirePng(crc32(contents.subarray(offset + 4, crcOffset)) === expectedCrc, `bad ${type} CRC`)
    chunkTypes.add(type)

    if (type === 'IHDR') {
      requirePng(offset === 8 && !ihdr, 'IHDR must be the first and only IHDR chunk')
      requirePng(length === 13, 'IHDR must contain 13 bytes')
      ihdr = {
        width: contents.readUInt32BE(dataStart),
        height: contents.readUInt32BE(dataStart + 4),
        bitDepth: contents[dataStart + 8],
        colorType: contents[dataStart + 9],
        interlaceMethod: contents[dataStart + 12],
      }
    }

    if (type === 'IDAT') {
      idatPayloads.push(contents.subarray(dataStart, crcOffset))
    }

    if (type === 'IEND') {
      requirePng(length === 0, 'IEND must be empty')
      requirePng(chunkEnd === contents.length, 'IEND must terminate the file')
      requirePng(ihdr, 'missing IHDR')

      const compressed = Buffer.concat(idatPayloads)
      requirePng(compressed.length > 0, 'missing IDAT data')
      requirePng(ihdr.bitDepth === 8, 'expected 8-bit samples')
      requirePng(ihdr.interlaceMethod === 0, 'interlaced data is unsupported')

      const channels = ihdr.colorType === 2 ? 3 : ihdr.colorType === 6 ? 4 : 0
      requirePng(channels > 0, `unsupported color type ${ihdr.colorType}`)
      const scanlines = inflateSync(compressed)
      const expectedLength = ihdr.height * (1 + ihdr.width * channels)
      requirePng(scanlines.length === expectedLength, 'unexpected decompressed scanline length')

      return { ...ihdr, chunkTypes }
    }

    offset = chunkEnd
  }

  throw new Error('Invalid PNG: missing IEND')
}

describe('PWA icons', () => {
  it.each([
    ['icon-192.png', 192, 6],
    ['icon-512.png', 512, 6],
    ['icon-maskable-512.png', 512, 2],
  ])('provides %s at its declared size and color type', async (filename, size, colorType) => {
    const contents = await readFile(new URL(`../public/icons/${filename}`, testModuleUrl))
    const image = parsePng(contents)

    expect(image.width).toBe(size)
    expect(image.height).toBe(size)
    expect(image.colorType).toBe(colorType)
    expect(image.bitDepth).toBe(8)
    expect(image.interlaceMethod).toBe(0)
  })

  it('provides the approved accessible SVG favicon', async () => {
    const contents = await readFile(new URL('../public/favicon.svg', testModuleUrl), 'utf8')

    expect(contents).toContain('<title id="title">IGoNotes</title>')
    expect(contents).toContain('fill="#2563eb"')
    expect(contents).toContain('aria-labelledby="title"')
  })

  it('provides the maskable icon without transparency', async () => {
    const contents = await readFile(new URL('../public/icons/icon-maskable-512.png', testModuleUrl))
    const image = parsePng(contents)

    expect(image.colorType).toBe(2)
    expect(image.chunkTypes).not.toContain('tRNS')
  })
})
```

- [ ] **Step 2: Run the icon test and verify it fails**

Run:

```bash
npm test -- --run src/pwa-icons.test.js
```

Working directory: `web`

Expected: FAIL with `ENOENT` for `public/icons/icon-192.png`; the existing Vite favicon also lacks the approved title and blue background.

- [ ] **Step 3: Replace the Vite favicon with the approved monogram**

Replace `web/public/favicon.svg` with:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 192 192" role="img" aria-labelledby="title">
  <title id="title">IGoNotes</title>
  <rect width="192" height="192" rx="42" fill="#2563eb"/>
  <path d="M49 51h94v90H49z" fill="none" stroke="#dbeafe" stroke-width="9" stroke-linejoin="round"/>
  <path d="M70 73h22M81 73v48M70 121h22" fill="none" stroke="#fff" stroke-width="10" stroke-linecap="round"/>
  <path d="M119 82c-14 0-21 8-21 20s7 20 21 20c7 0 12-2 16-5v-16h-16" fill="none" stroke="#bfdbfe" stroke-width="9" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
```

- [ ] **Step 4: Create the icon directory**

Verify the parent directory first:

```bash
ls public
```

Working directory: `web`

Expected: the directory lists `favicon.svg` and `icons.svg`.

Then run:

```bash
mkdir -p public/icons
```

Working directory: `web`

Expected: exit code `0`.

- [ ] **Step 5: Render the standard PNG icons from the SVG**

Run:

```bash
convert -background none -density 384 public/favicon.svg -resize 192x192 public/icons/icon-192.png
```

Working directory: `web`

Expected: ImageMagick exits with code `0` and creates a 192px transparent-corner PNG.

Run:

```bash
convert -background none -density 384 public/favicon.svg -resize 512x512 public/icons/icon-512.png
```

Working directory: `web`

Expected: ImageMagick exits with code `0` and creates a 512px transparent-corner PNG.

- [ ] **Step 6: Render the full-bleed maskable PNG**

Run:

```bash
convert -background '#2563eb' -density 384 public/favicon.svg -resize 512x512 -alpha remove -alpha off public/icons/icon-maskable-512.png
```

Working directory: `web`

Expected: ImageMagick exits with code `0`; transparent rounded corners are flattened to the same blue so an OS mask cannot expose empty corners.

- [ ] **Step 7: Re-run the icon validation test**

Run:

```bash
npm test -- --run src/pwa-icons.test.js
```

Working directory: `web`

Expected: PASS, 5 tests passed. The fifth test verifies maskable opacity, while the PNG cases also validate complete chunk structure and image data.

### Task 3: Serve The Manifest With Its Required MIME Type

**Files:**
- Create: `internal/handlers/static_handler_test.go`
- Modify: `internal/handlers/static_handler.go:34-40`

- [ ] **Step 1: Write the failing static asset test**

Create `internal/handlers/static_handler_test.go`:

```go
package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesPWAAssets(t *testing.T) {
	manifest := []byte(`{"name":"IGoNotes"}`)
	icon := []byte("\x89PNG\r\n\x1a\nicon")
	handler := NewSPAHandler(fstest.MapFS{
		"manifest.webmanifest": &fstest.MapFile{Data: manifest},
		"icons/icon-192.png":  &fstest.MapFile{Data: icon},
	})

	tests := []struct {
		name        string
		path        string
		contentType string
		body        []byte
	}{
		{
			name:        "manifest",
			path:        "/manifest.webmanifest",
			contentType: "application/manifest+json",
			body:        manifest,
		},
		{
			name:        "icon",
			path:        "/icons/icon-192.png",
			contentType: "image/png",
			body:        icon,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := recorder.Body.Bytes(); string(got) != string(test.body) {
				t.Fatalf("body = %q, want %q", got, test.body)
			}
		})
	}
}
```

- [ ] **Step 2: Run the static asset test and verify it fails**

Run:

```bash
go test ./internal/handlers -run '^TestSPAHandlerServesPWAAssets$' -count=1
```

Working directory: repository root.

Expected: FAIL because `manifest.webmanifest` is currently returned as `text/plain; charset=utf-8`, not `application/manifest+json`. The icon subtest passes.

- [ ] **Step 3: Set the manifest content type before serving the file**

In `internal/handlers/static_handler.go`, update the existing successful-file branch to:

```go
	if err == nil {
		f.Close()
		if p == "manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		// Файл существует, отдаем его
		http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, r)
		return
	}
```

Do not add cache headers or special handling for any other static file.

- [ ] **Step 4: Format and re-run the focused Go test**

Run:

```bash
gofmt -w internal/handlers/static_handler.go internal/handlers/static_handler_test.go
```

Working directory: repository root.

Expected: exit code `0`.

Run:

```bash
go test ./internal/handlers -run '^TestSPAHandlerServesPWAAssets$' -count=1
```

Working directory: repository root.

Expected: PASS for both the manifest and icon subtests.

### Task 4: Verify The Embedded Installable Application

**Files:**
- Verify: `web/dist/manifest.webmanifest`
- Verify: `web/dist/favicon.svg`
- Verify: `web/dist/icons/icon-192.png`
- Verify: `web/dist/icons/icon-512.png`
- Verify: `web/dist/icons/icon-maskable-512.png`
- Verify: all modified source and test files

- [ ] **Step 1: Run the complete frontend test suite**

With Node 24.20.0 active as described in the execution prerequisite, run:

```bash
npm test
```

Working directory: `web`

Expected: all Vitest suites pass, including `pwa-metadata.test.js` and `pwa-icons.test.js`.

- [ ] **Step 2: Build the production frontend**

With Node 24.20.0 active, run:

```bash
npm run build
```

Working directory: `web`

Expected: Vite completes successfully and writes `web/dist`.

- [ ] **Step 3: Verify Vite copied every PWA resource**

Run each command from the repository root:

```bash
test -f web/dist/manifest.webmanifest
test -f web/dist/favicon.svg
test -f web/dist/icons/icon-192.png
test -f web/dist/icons/icon-512.png
test -f web/dist/icons/icon-maskable-512.png
```

Expected: every command exits with code `0`.

- [ ] **Step 4: Run the complete Go test suite against the new embedded dist**

Run:

```bash
go test ./...
```

Working directory: repository root.

Expected: all Go packages pass.

- [ ] **Step 5: Build the complete application**

With Node 24.20.0 active, run:

```bash
make all
```

Working directory: repository root.

Expected: frontend build and Go binary build complete successfully; `builds/igonotes` exists.

- [ ] **Step 6: Check the final diff for formatting errors**

Run:

```bash
git diff --check
```

Working directory: repository root.

Expected: exit code `0` with no output.

- [ ] **Step 7: Verify Chromium installability manually**

Verify the approved temporary parent directory exists:

```bash
ls "/tmp/opencode"
```

Working directory: repository root.

Expected: `/tmp/opencode` exists and its contents are listed.

In the same shell, create isolated home and configuration directories:

```bash
runtime_home=$(mktemp -d "/tmp/opencode/igonotes-pwa-home.XXXXXX")
runtime_config=$(mktemp -d "/tmp/opencode/igonotes-pwa-config.XXXXXX")
```

Expected: both commands exit with code `0` and assign newly created temporary directories.

Check that port 18080 is not already in use:

```bash
ss -ltn 'sport = :18080'
```

Expected: the command prints only its header, with no listening socket row for port 18080. If a listener is present, stop and do not continue until port 18080 is available.

Start the application without automatic browser launch, using the isolated home, configuration directory, and explicit port:

```bash
HOME="$runtime_home" ./builds/igonotes --config "$runtime_config" --port 18080 --no-browser
```

Working directory: repository root.

Expected: the server listens on `http://127.0.0.1:18080`.

Setting both `HOME` and `--config` keeps the configuration, metadata database, placeholder base and synchronization state, and notes away from user paths.

Open `http://127.0.0.1:18080` in desktop Chromium and verify:

- DevTools → Application → Manifest reports no installability errors.
- The application name is `IGoNotes` and all three icons render correctly.
- Chromium exposes its install action after its engagement criteria are met.
- Installing opens IGoNotes in a standalone window without the normal browser toolbar.
- Closing the Go server makes the installed shortcut unavailable, as documented; the shortcut remains bound to its installed origin and port and does not attempt to launch the binary.

After the checks, stop the server cleanly with `Ctrl+C` and confirm it no longer listens on port 18080.

Do not create a git commit unless the user explicitly requests one.
