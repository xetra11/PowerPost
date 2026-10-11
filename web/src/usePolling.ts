import { useCallback, useEffect, useRef, useState } from 'react'

/**
 * Loads data and keeps it fresh: every `intervalMs` while `active(data)` is true,
 * otherwise every 5 × intervalMs. Data set via setData (e.g. after an action)
 * counts immediately, so polling speeds up as soon as work starts.
 */
export function usePolling<T>(load: () => Promise<T>, intervalMs: number, active: (data: T | null) => boolean = () => true) {
  const [data, setDataState] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loadRef = useRef(load)
  loadRef.current = load
  const activeRef = useRef(active)
  activeRef.current = active
  const dataRef = useRef<T | null>(null)
  const lastFetch = useRef(0)

  const setData = useCallback((d: T) => {
    dataRef.current = d
    setDataState(d)
  }, [])

  const reload = useCallback(async () => {
    lastFetch.current = Date.now()
    try {
      const d = await loadRef.current()
      setData(d)
      setError(null)
      return d
    } catch (e) {
      setError((e as Error).message)
      return null
    }
  }, [setData])

  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      const due = activeRef.current(dataRef.current) || Date.now() - lastFetch.current >= intervalMs * 5
      if (due) await reload()
      if (!stopped) timer = setTimeout(tick, intervalMs)
    }
    reload().then(() => {
      if (!stopped) timer = setTimeout(tick, intervalMs)
    })
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [reload, intervalMs])

  return { data, setData, error, reload }
}
