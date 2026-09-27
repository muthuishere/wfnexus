import { useEffect, useState } from 'react'
import { api, type Category } from '../api'

// The category list is fixed on the server and asked for once per page load,
// so every picker and every grouping shows the same labels in the same order.
let cache: Promise<Category[]> | undefined

export function useCategories(): Category[] {
  const [cats, setCats] = useState<Category[]>([])
  useEffect(() => {
    cache ??= api.categories().catch(() => { cache = undefined; return [] })
    let live = true
    cache.then(c => { if (live) setCats(c) })
    return () => { live = false }
  }, [])
  return cats
}

export function labelOf(cats: Category[], id?: string): string {
  if (!id) return 'Uncategorised'
  return cats.find(c => c.id === id)?.label || id
}
