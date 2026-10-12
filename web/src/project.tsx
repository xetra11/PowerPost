import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { api, type Project } from './api'

interface ProjectState {
  projects: Project[] | null
  /** The project the UI works in; null while loading. */
  current: Project | null
  select: (id: number) => void
  reload: () => Promise<Project[]>
  error: string | null
}

const Ctx = createContext<ProjectState | null>(null)
const storageKey = 'pp.project'

function loadId(): number | null {
  try {
    const v = Number(localStorage.getItem(storageKey))
    return v > 0 ? v : null
  } catch {
    return null
  }
}

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<Project[] | null>(null)
  const [selected, setSelected] = useState<number | null>(loadId)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    try {
      const list = await api.projects()
      setProjects(list)
      setError(null)
      return list
    } catch (e) {
      setError((e as Error).message)
      return []
    }
  }, [])
  useEffect(() => {
    reload()
  }, [reload])

  const select = useCallback((id: number) => {
    setSelected(id)
    try {
      localStorage.setItem(storageKey, String(id))
    } catch {
      /* storage unavailable */
    }
  }, [])

  const current = projects ? (projects.find((p) => p.id === selected) ?? projects[0] ?? null) : null
  return <Ctx.Provider value={{ projects, current, select, reload, error }}>{children}</Ctx.Provider>
}

export function useProjects(): ProjectState {
  const v = useContext(Ctx)
  if (!v) throw new Error('useProjects outside ProjectProvider')
  return v
}

/** Switches the UI to the project of a post opened by link (e.g. a review link from Claude). */
export function useFollowProject(projectId: number | undefined) {
  const { current, select } = useProjects()
  useEffect(() => {
    if (projectId && current && current.id !== projectId) select(projectId)
  }, [projectId, current, select])
}
