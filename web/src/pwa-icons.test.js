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
