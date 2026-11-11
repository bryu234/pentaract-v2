import { useEffect, useState } from 'react'

export type FolderLocation = { id: string | null; name: string }
type Navigation = {
  tab: 'files' | 'trash' | 'settings'
  parents: FolderLocation[]
  search: string
}

const stateKey = 'pentaractNavigation'
const root = (): Navigation => ({ tab: 'files', parents: [{ id: null, name: 'Файлы' }], search: '' })

function readNavigation(): Navigation {
  const saved = window.history.state?.[stateKey]
  if (saved?.version !== 1 || !['files', 'trash', 'settings'].includes(saved.tab) ||
      typeof saved.search !== 'string' || !Array.isArray(saved.parents) || !saved.parents.length ||
      saved.parents[0]?.id !== null || !saved.parents.every((p: FolderLocation) =>
        p && (p.id === null || typeof p.id === 'string') && typeof p.name === 'string')) return root()
  return { tab: saved.tab, parents: saved.parents, search: saved.search }
}

// Keep the existing URL (including any query parameters) and unrelated history state.
// replaceState initializes the entry without adding an extra Back step on page load.
export function useWorkspaceNavigation() {
  const [navigation, setNavigation] = useState<Navigation>(readNavigation)
  useEffect(() => {
    const current = readNavigation()
    window.history.replaceState({ ...window.history.state, [stateKey]: { version: 1, ...current } }, '')
    const onPopState = () => setNavigation(readNavigation())
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const navigate = (next: Navigation, replace = false) => {
    if (JSON.stringify(readNavigation()) === JSON.stringify(next)) return
    const state = { ...window.history.state, [stateKey]: { version: 1, ...next } }
    if (replace) window.history.replaceState(state, '')
    else window.history.pushState(state, '')
    setNavigation(next)
  }
  return {
    ...navigation,
    setTab: (tab: Navigation['tab']) => navigate({ ...navigation, tab }),
    setParents: (parents: FolderLocation[]) => navigate({ ...navigation, tab: 'files', parents, search: '' }),
    // Typing a search updates this entry instead of adding a Back step for every letter.
    setSearch: (search: string) => navigate({ ...navigation, search }, true),
  }
}
