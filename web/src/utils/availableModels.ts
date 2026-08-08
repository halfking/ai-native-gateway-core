import type { AvailableModelsResponse, PopularModel, AvailableVersion } from '../api'

export function projectAvailableModels(data: AvailableModelsResponse): PopularModel[] {
  const byCanonical = new Map<string, PopularModel>()
  const add = (model: PopularModel) => {
    const key = model.canonical_name.trim().toLowerCase()
    if (!key || byCanonical.has(key)) return
    byCanonical.set(key, model)
  }

  for (const model of data.popular || []) add(model)
  for (const family of data.families || []) {
    for (const version of family.versions || []) {
      add({
        canonical_name: version.canonical_name,
        display_name: version.display_name || version.canonical_name,
        source: 'catalog',
      })
    }
  }
  return Array.from(byCanonical.values())
}

export function flattenAvailableVersions(data: AvailableModelsResponse): AvailableVersion[] {
  const seen = new Set<string>()
  const result: AvailableVersion[] = []
  for (const family of data.families || []) {
    for (const version of family.versions || []) {
      const key = version.canonical_name.trim().toLowerCase()
      if (!key || seen.has(key)) continue
      seen.add(key)
      result.push(version)
    }
  }
  return result
}
