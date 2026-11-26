/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

// index.html paints a boot shell next to #root and runs an inline watchdog
// that explains a slow or failed start. Both work before React and i18n.
const html = readFileSync(
  resolve(import.meta.dirname, '../../../index.html'),
  'utf8'
)
const watchdog =
  html.match(/<script id="boot-watchdog">([\s\S]*?)<\/script>/)?.[1] ?? ''
const shellStyle = html.match(/<style>([\s\S]*?)<\/style>/)?.[1] ?? ''
const bodyMarkup = html.match(/<body>([\s\S]*?)<\/body>/)?.[1] ?? ''

const reload = vi.fn()

function byId(id: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`#${id}`)
  if (!element) throw new Error(`#${id} is missing from index.html`)
  return element
}

const message = () => byId('boot-shell-message').textContent
const reloadButton = () => byId('boot-shell-reload') as HTMLButtonElement
const spinner = () => byId('boot-shell-spinner')

function startWatchdog() {
  new Function(watchdog)()
}

/** React's first render into #root; the watchdog stops on this mutation. */
async function mountApp() {
  byId('root').append(document.createElement('div'))
  await Promise.resolve()
}

function failStaticScript(src = '/static/js/index.3f2a.js') {
  const script = document.createElement('script')
  script.src = src
  document.head.append(script)
  script.dispatchEvent(new Event('error'))
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('location', { ...window.location, reload })
  localStorage.clear()
  document.body.innerHTML = bodyMarkup
  // jsdom has no `noModule`; every supported browser does.
  Object.defineProperty(HTMLScriptElement.prototype, 'noModule', {
    configurable: true,
    value: false,
  })
})

afterEach(async () => {
  // Let every watchdog of the test see a mount, so it removes its listeners.
  if (!document.querySelector('#root')?.firstChild) await mountApp()
  Reflect.deleteProperty(HTMLScriptElement.prototype, 'noModule')
  document.body.innerHTML = ''
  document.head.querySelectorAll('script').forEach((script) => script.remove())
  localStorage.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  reload.mockReset()
})

describe('boot shell', () => {
  test('sits after #root and is hidden by CSS once #root has children', async () => {
    expect(bodyMarkup.indexOf('id="root"')).toBeLessThan(
      bodyMarkup.indexOf('id="boot-shell"')
    )
    expect(byId('root').innerHTML).toBe('')
    expect(shellStyle).toMatch(/#root:not\(:empty\) ~ #boot-shell/)
    expect(document.querySelector('#root:not(:empty) ~ #boot-shell')).toBe(null)

    await mountApp()

    expect(document.querySelector('#root:not(:empty) ~ #boot-shell')).toBe(
      byId('boot-shell')
    )
  })

  test('shows the cached site name and a spinner while the app loads', () => {
    localStorage.setItem('status', JSON.stringify({ system_name: 'Acme AI' }))

    startWatchdog()

    expect(byId('boot-shell-name').textContent).toBe('Acme AI')
    expect(message()).toBe('')
    expect(spinner().hidden).toBe(false)
    expect(reloadButton().hidden).toBe(true)
  })

  test('says the network is slow at 15 s and offers a reload at 45 s, never reloading by itself', () => {
    startWatchdog()

    vi.advanceTimersByTime(14_999)
    expect(message()).toBe('')
    vi.advanceTimersByTime(1)
    expect(message()).toBe('The network is slow, still loading…')
    expect(reloadButton().hidden).toBe(true)

    vi.advanceTimersByTime(30_000)
    expect(reloadButton().hidden).toBe(false)
    vi.advanceTimersByTime(10 * 60_000)
    expect(reload).not.toHaveBeenCalled()

    reloadButton().click()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('a failed app script or stylesheet shows the resource message with a reload button', () => {
    startWatchdog()

    failStaticScript()

    expect(message()).toBe('Some page files failed to load')
    expect(spinner().hidden).toBe(true)
    expect(reloadButton().hidden).toBe(false)
    expect(reloadButton().textContent).toBe('Reload')
    expect(reload).not.toHaveBeenCalled()
  })

  test('Chinese visitors read the messages in Chinese', () => {
    localStorage.setItem('i18nextLng', 'zhCN')
    startWatchdog()

    const link = document.createElement('link')
    link.rel = 'stylesheet'
    link.href = '/static/css/index.9c1d.css'
    document.head.append(link)
    link.dispatchEvent(new Event('error'))

    expect(message()).toBe('页面资源加载失败')
    expect(reloadButton().textContent).toBe('重新加载')
    link.remove()
  })

  test('resources outside /static/ (analytics, icons) do not count as a failed start', () => {
    startWatchdog()

    failStaticScript('https://analytics.example.com/script.js')
    const icon = document.createElement('link')
    icon.rel = 'icon'
    icon.href = '/static/favicon.ico'
    document.head.append(icon)
    icon.dispatchEvent(new Event('error'))
    window.dispatchEvent(
      new ErrorEvent('error', {
        message: 'Script error.',
        filename: 'https://analytics.example.com/script.js',
      })
    )

    expect(message()).toBe('')
    expect(reloadButton().hidden).toBe(true)
    icon.remove()
  })

  test('an error thrown by the app bundles before the first render shows the start-up message', () => {
    startWatchdog()

    window.dispatchEvent(
      new ErrorEvent('error', {
        message: 'Uncaught TypeError: x is not a function',
        filename: 'http://localhost/static/js/index.3f2a.js',
      })
    )

    expect(message()).toBe('The page failed to start')
    expect(reloadButton().hidden).toBe(false)
  })

  test('a browser without the features the bundle needs is told to upgrade, without a reload button', () => {
    const at = Object.getOwnPropertyDescriptor(Array.prototype, 'at')
    Reflect.deleteProperty(Array.prototype, 'at')
    try {
      startWatchdog()
    } finally {
      if (at) Object.defineProperty(Array.prototype, 'at', at)
    }

    expect(message()).toBe(
      'This browser is too old. Please update it or open the page in your system browser.'
    )
    // The bundle then fails to parse; the advice stays.
    window.dispatchEvent(
      new ErrorEvent('error', {
        message: 'SyntaxError',
        filename: 'http://localhost/static/js/index.3f2a.js',
      })
    )
    vi.advanceTimersByTime(60_000)
    expect(message()).toBe(
      'This browser is too old. Please update it or open the page in your system browser.'
    )
    expect(reloadButton().hidden).toBe(true)
  })

  test('stops as soon as the app renders', async () => {
    startWatchdog()

    await mountApp()
    failStaticScript()
    vi.advanceTimersByTime(60_000)

    expect(message()).toBe('')
    expect(reloadButton().hidden).toBe(true)
    expect(spinner().hidden).toBe(true)
  })

  test('without JavaScript the shell is hidden and a bilingual notice shows', () => {
    expect(html).toMatch(
      /<noscript>\s*<style>\s*#boot-shell {\s*display: none;/
    )
    const notice = bodyMarkup.match(/<noscript>([\s\S]*?)<\/noscript>/)?.[1]
    expect(notice).toMatch(/JavaScript/)
    expect(notice).toMatch(/需要启用 JavaScript/)
  })
})
