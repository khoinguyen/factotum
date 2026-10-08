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
  wave?: number
  unblocks?: number
  milestone?: boolean
  score?: number
  origin?: string
  origin_title?: string
}

export type TaskLink = {
  id: string
  title: string
  class: string
  chip: string
  reason?: string
  detail?: string
  url: string
}

export type ArtifactView = {
  id: string
  kind: string
  title: string
  brief?: string
  body?: string
  url: string
}

export type IdeaView = {
  id: string
  kind: string
  title: string
  description?: string
  repo?: string
  state: string
  chip: string
  url: string
  total: number
  done: number
  active: number
  blocked: number
  tasks?: TaskLink[]
  artifacts?: ArtifactView[]
}

export type TaskGroup = {
  idea_id?: string
  idea_title?: string
  ungrouped?: boolean
  tasks: TaskView[]
}

export type UpdateView = {
  time: string
  summary: string
}

export type LinkView = {
  kind: string
  url: string
  title?: string
}

export type NoteView = {
  author: string
  body: string
  created_at: string
  system?: boolean
  links: LinkView[]
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
  in_flight_groups: TaskGroup[]
  waiting_groups: TaskGroup[]
  ideas: {
    blocked: IdeaView[]
    active: IdeaView[]
    finished: IdeaView[]
    captured: IdeaView[]
  }
  updates: UpdateView[]
  flags: string[]
}

export type TaskDetail = TaskLink & {
  kind: string
  repo?: string
  assignee?: string
  groomed: boolean
  description?: string
  acceptance: string[]
  deps: TaskLink[]
  dependents: TaskLink[]
  notes: NoteView[]
  artifacts: ArtifactView[]
  created_at: string
  updated_at: string
  origin?: TaskLink
}

export type ArtifactDetail = ArtifactView & {
  links: LinkView[]
  created_at: string
  updated_at: string
  attached_to?: TaskLink
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { cache: "no-store" })
  if (!res.ok) {
    throw new Error(`${path} failed: ${res.status}`)
  }
  return (await res.json()) as T
}

export async function fetchSnapshot(): Promise<Snapshot> {
  return normalizeSnapshot(await getJSON<Snapshot>("/api/snapshot"))
}

export async function fetchIdea(id: string): Promise<IdeaView> {
  const idea = await getJSON<IdeaView>(`/api/idea/${encodeURIComponent(id)}`)
  return {
    ...idea,
    tasks: idea.tasks ?? [],
    artifacts: idea.artifacts ?? [],
  }
}

export async function fetchTask(id: string): Promise<TaskDetail> {
  const task = await getJSON<TaskDetail>(`/api/task/${encodeURIComponent(id)}`)
  return {
    ...task,
    acceptance: task.acceptance ?? [],
    deps: task.deps ?? [],
    dependents: task.dependents ?? [],
    notes: (task.notes ?? []).map((note) => ({ ...note, links: note.links ?? [] })),
    artifacts: task.artifacts ?? [],
  }
}

export async function fetchArtifact(
  kind: "memory" | "doc",
  id: string,
): Promise<ArtifactDetail> {
  const artifact = await getJSON<ArtifactDetail>(
    `/api/${kind}/${encodeURIComponent(id)}`,
  )
  return { ...artifact, links: artifact.links ?? [] }
}

// normalizeSnapshot coerces every list to an array. The server sends [] for
// empty lanes, but JSON from a network boundary is untrusted and a null here
// used to blank the whole page via .length/.map.
export function normalizeSnapshot(s: Snapshot): Snapshot {
  return {
    ...s,
    next_agent: s.next_agent ?? [],
    next_human: s.next_human ?? [],
    in_flight: s.in_flight ?? [],
    waiting: s.waiting ?? [],
    in_flight_groups: s.in_flight_groups ?? [],
    waiting_groups: s.waiting_groups ?? [],
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
