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
  return (await res.json()) as Snapshot
}
