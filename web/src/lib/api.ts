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

// PipelineView is one factory pipeline: its origin capture, phase, open gate,
// the tasks it produced, and the exact command that clears the gate.
export type PipelineView = {
  id: string
  capture: string
  capture_title?: string
  capture_url: string
  state: string
  gate?: string
  command?: string
  session?: string
  error?: string
  produced: TaskLink[]
  updated_at?: string
}

export type TaskGroup = {
  idea_id?: string
  idea_title?: string
  idea_state?: string
  idea_kind?: string
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
  pipelines: PipelineView[]
  updates: UpdateView[]
  flags: string[]
}

// SessionTask is one task a chief session drove to done.
export type SessionTask = {
  id: string
  title: string
  url: string
}

// SessionPR is one pull request recorded on a task the session delivered.
export type SessionPR = {
  url: string
  title?: string
}

// SessionDelivered is the work one session drove, read from its events.
export type SessionDelivered = {
  tasks_done: number
  prs_merged: number
  captures_advanced: number
  tasks: SessionTask[]
  prs: SessionPR[]
}

// SessionView is one chief session: its lifecycle and the work it delivered.
export type SessionView = {
  id: string
  state: string
  started_at?: string
  stopped_at?: string
  duration_seconds: number
  reason?: string
  successor?: string
  delivered: SessionDelivered
}

// SessionsDoc is the /api/sessions document.
export type SessionsDoc = {
  sessions: SessionView[]
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
  grouped_under: TaskLink[]
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
  // Captures are the captures that link back to a prompt artifact, the one list
  // its page shows whether grooming produced one or many.
  captures?: TaskLink[]
}

// NotificationView is one completed prompt grooming: the prompt artifact to
// open and how many captures linked back to it.
export type NotificationView = {
  id: string
  title: string
  url: string
  captures: number
  completed_at?: string
}

// NotificationsDoc is the /api/notifications document.
export type NotificationsDoc = {
  notifications: NotificationView[]
}

// CaptureKind is a human capture the write side accepts: an idea or a bug.
export type CaptureKind = "idea" | "bug"

// CaptureConfig is the /api/capture read: whether the form should be shown and
// the project a capture would be attributed to.
export type CaptureConfig = {
  enabled: boolean
  project?: string
}

// CaptureResult is a stored capture and the detail URL to navigate to.
export type CaptureResult = {
  id: string
  kind: string
  url: string
}

// PromptResult is a stored prompt document artifact and the doc URL to open.
export type PromptResult = {
  id: string
  url: string
}

// CaptureError carries the HTTP status of a rejected capture so the page can
// tell a bad token (401) from a disabled server (403) from a bad request (400).
export class CaptureError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = "CaptureError"
    this.status = status
  }
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

export async function fetchCaptureConfig(): Promise<CaptureConfig> {
  return getJSON<CaptureConfig>("/api/capture")
}

// submitCapture posts a natural-language sentence to the token-gated write
// side. The shared token travels in the Authorization header, the only
// credential the server accepts.
export async function submitCapture(
  text: string,
  kind: CaptureKind,
  token: string,
): Promise<CaptureResult> {
  const res = await fetch("/api/capture", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify({ kind, text }),
  })
  if (!res.ok) {
    throw new CaptureError(res.status, await captureErrorMessage(res))
  }
  return (await res.json()) as CaptureResult
}

// submitPrompt posts a free-form prompt to the token-gated write side. The
// server stores it as a document artifact and notifies the chief; the app opens
// the document when it lands.
export async function submitPrompt(text: string, token: string): Promise<PromptResult> {
  const res = await fetch("/api/prompt", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify({ text }),
  })
  if (!res.ok) {
    throw new CaptureError(res.status, await captureErrorMessage(res))
  }
  return (await res.json()) as PromptResult
}

// captureErrorMessage extracts the server's error text, falling back to the
// status when the body is not the expected JSON.
async function captureErrorMessage(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    if (body.error) return body.error
  } catch {
    // fall through to the generic message
  }
  return `capture failed: ${res.status}`
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
    pipelines: (s.pipelines ?? []).map((pipeline) => ({
      ...pipeline,
      produced: pipeline.produced ?? [],
    })),
    ideas: {
      blocked: s.ideas?.blocked ?? [],
      active: s.ideas?.active ?? [],
      finished: s.ideas?.finished ?? [],
      captured: s.ideas?.captured ?? [],
    },
  }
}

// normalizeNotifications coerces the feed to an array: the server sends [] for
// an empty feed, but JSON across a network boundary is untrusted and a null
// would blank the toaster via .filter.
export function normalizeNotifications(doc: NotificationsDoc): NotificationsDoc {
  return { notifications: doc.notifications ?? [] }
}

// normalizeSessions coerces every list to an array: the server sends [] for
// empty lanes, but JSON across a network boundary is untrusted and a null would
// blank the page via .length/.map.
export function normalizeSessions(doc: SessionsDoc): SessionsDoc {
  return {
    sessions: (doc.sessions ?? []).map((session) => ({
      ...session,
      delivered: {
        tasks_done: session.delivered?.tasks_done ?? 0,
        prs_merged: session.delivered?.prs_merged ?? 0,
        captures_advanced: session.delivered?.captures_advanced ?? 0,
        tasks: session.delivered?.tasks ?? [],
        prs: session.delivered?.prs ?? [],
      },
    })),
  }
}
