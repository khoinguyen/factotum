export type TaskView = {
  id: string
  title: string
  class: string
  chip: string
  url: string
  repo?: string
  reason?: string
  detail?: string
  assignee?: string
}

export type IdeaView = {
  id: string
  title: string
  state: string
  chip: string
  url: string
  total: number
  done: number
  active: number
  blocked: number
}

export type UpdateView = {
  time: string
  summary: string
}

export type Snapshot = {
  title: string
  project: string
  snapshot: string
  stats: {
    scope: number
    ideas: number
    done: number
    ready_agent: number
    ready_human: number
    blocked: number
    cycles: number
    waves: number
  }
  next_agent: TaskView[]
  next_human: TaskView[]
  in_flight: TaskView[]
  waiting: TaskView[]
  ideas: {
    blocked: IdeaView[]
    active: IdeaView[]
    finished: IdeaView[]
    captured: IdeaView[]
  }
  updates: UpdateView[]
  flags: string[]
}

export async function fetchSnapshot(): Promise<Snapshot> {
  const res = await fetch("/api/snapshot", { cache: "no-store" })
  if (!res.ok) {
    throw new Error(`snapshot request failed: ${res.status}`)
  }
  return normalizeSnapshot((await res.json()) as Snapshot)
}

// normalizeSnapshot coerces every list to an array. The server now sends [] for
// empty lanes, but JSON from a network boundary is untrusted and a null here
// used to blank the whole page via .length/.map.
export function normalizeSnapshot(s: Snapshot): Snapshot {
  return {
    ...s,
    next_agent: s.next_agent ?? [],
    next_human: s.next_human ?? [],
    in_flight: s.in_flight ?? [],
    waiting: s.waiting ?? [],
    updates: s.updates ?? [],
    flags: s.flags ?? [],
    ideas: {
      blocked: s.ideas?.blocked ?? [],
      active: s.ideas?.active ?? [],
      finished: s.ideas?.finished ?? [],
      captured: s.ideas?.captured ?? [],
    },
  }
}
