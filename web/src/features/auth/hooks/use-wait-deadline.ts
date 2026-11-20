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
import { useCallback, useEffect, useState } from 'react'

/**
 * A wait the server asked for, counted down to a fixed deadline. The seconds
 * left are read from the clock on every tick and whenever the tab becomes
 * visible again, so a background or sleeping tab, whose timers the browser
 * slows down, never shows a wait longer than the real one.
 */
export function useWaitDeadline() {
  const [deadline, setDeadline] = useState<number | null>(null)
  const [secondsLeft, setSecondsLeft] = useState(0)

  const start = useCallback((seconds: number) => {
    const wait = Math.max(0, Math.ceil(seconds))
    setDeadline(Date.now() + wait * 1000)
    setSecondsLeft(wait)
  }, [])

  const stop = useCallback(() => {
    setDeadline(null)
    setSecondsLeft(0)
  }, [])

  useEffect(() => {
    if (deadline === null) return
    const update = () => {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setSecondsLeft(left)
      if (left === 0) setDeadline(null)
    }
    update()
    const timer = window.setInterval(update, 1000)
    document.addEventListener('visibilitychange', update)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', update)
    }
  }, [deadline])

  return { secondsLeft, active: secondsLeft > 0, start, stop }
}
