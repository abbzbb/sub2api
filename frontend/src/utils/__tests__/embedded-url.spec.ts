import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { buildEmbeddedUrl, detectTheme } from '../embedded-url'

const dir = dirname(fileURLToPath(import.meta.url))
const customPageViewSource = readFileSync(resolve(dir, '../../views/user/CustomPageView.vue'), 'utf8')

describe('embedded-url', () => {
  const originalLocation = window.location

  beforeEach(() => {
    Object.defineProperty(window, 'location', {
      value: {
        origin: 'https://app.example.com',
        href: 'https://app.example.com/user/purchase',
      },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    })
    document.documentElement.classList.remove('dark')
    vi.restoreAllMocks()
  })

  it('adds embedded query parameters including locale and source context', () => {
    const result = buildEmbeddedUrl(
      'https://pay.example.com/checkout?plan=pro',
      42,
      'dark',
      'zh-CN',
    )

    const url = new URL(result)
    expect(url.searchParams.get('plan')).toBe('pro')
    expect(url.searchParams.get('user_id')).toBe('42')
    expect(url.searchParams.has('token')).toBe(false)
    expect(url.searchParams.get('theme')).toBe('dark')
    expect(url.searchParams.get('lang')).toBe('zh-CN')
    expect(url.searchParams.get('ui_mode')).toBe('embedded')
    expect(url.searchParams.get('src_host')).toBe('https://app.example.com')
    expect(url.searchParams.get('src_url')).toBe('https://app.example.com/user/purchase')
  })

  it('never appends auth JWT query parameters', () => {
    const result = buildEmbeddedUrl(
      'https://pay.example.com/checkout',
      42,
      'light',
      'en',
    )

    const url = new URL(result)
    expect(url.searchParams.has('token')).toBe(false)
    expect(url.searchParams.has('jwt')).toBe(false)
    expect(url.searchParams.has('access_token')).toBe(false)
    expect(result).not.toMatch(/[?&]token=/)
  })

  it('keeps the custom page iframe sandbox isolated from its parent origin', () => {
    const sandbox = customPageViewSource.match(/<iframe[\s\S]*?sandbox="([^"]+)"/)?.[1]

    expect(sandbox?.split(/\s+/)).toEqual(['allow-scripts', 'allow-forms', 'allow-popups'])
    expect(sandbox).not.toContain('allow-same-origin')
    expect(sandbox).not.toContain('allow-popups-to-escape-sandbox')
  })

  it('omits optional params when they are empty', () => {
    const result = buildEmbeddedUrl('https://pay.example.com/checkout', undefined, 'light')

    const url = new URL(result)
    expect(url.searchParams.get('theme')).toBe('light')
    expect(url.searchParams.get('ui_mode')).toBe('embedded')
    expect(url.searchParams.has('user_id')).toBe(false)
    expect(url.searchParams.has('token')).toBe(false)
    expect(url.searchParams.has('lang')).toBe(false)
  })

  it('returns original string for invalid url input', () => {
    expect(buildEmbeddedUrl('not a url', 1)).toBe('not a url')
  })

  it('detects dark mode from document root class', () => {
    document.documentElement.classList.add('dark')
    expect(detectTheme()).toBe('dark')
  })
})
