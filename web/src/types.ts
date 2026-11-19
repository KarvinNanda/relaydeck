// JSON shapes of the daemon API. Keep in sync with:
//   internal/daemon/state.go (State, WorkspaceState, FolderState)
//   internal/daemon/cswappoll.go (Account)
//   internal/daemon/events.go (Event) and internal/server/ws.go (first frame)
//   internal/notify/router.go (Message, State)
//   internal/agent/manager.go (Status)
//   internal/server/api.go (sessionJSON)

export type AgentStatus = 'starting' | 'idle' | 'busy' | 'attention' | 'stopped'

export interface Account {
  cswap: boolean // cswap is installed and used (never on windows)
  known: boolean // the last cswap status call worked
  default: string // default account number, only when known
  email?: string
}

export interface FolderState {
  name: string
  path: string
  account?: string // never set on windows
  permission_mode: string
  session_id?: string
  status: AgentStatus
  // "trust" when the trust dialog is up, "waiting_for_prompt" when no input
  // box is visible; absent otherwise or when not running.
  screen?: 'trust' | 'waiting_for_prompt'
  on_default_login: boolean // a cswap switch would move this agent
  pending: number // unread: queued, held or nudged
  awaiting_approval: number // held at the hop limit
  external_pid?: number // a claude outside relaydeck has this folder or session open (#89)
}

export interface WorkspaceState {
  name: string
  folders: FolderState[]
}

export interface State {
  platform: string
  account: Account
  workspaces: WorkspaceState[]
}

export type MessageState =
  | 'queued'
  | 'held_external'
  | 'held_draft'
  | 'nudged'
  | 'awaiting_approval'
  | 'delivered'
  | 'rejected'

export interface Message {
  id: string
  workspace: string
  from: string
  to: string
  body: string // agent text: render only with text interpolation, never v-html
  hop: number
  created: string // RFC 3339
  state: MessageState
}

export interface Session {
  id: string
  modified: string // RFC 3339
  title: string
}

// Frames on /ws/events. The first one is always a full snapshot.
export type StateFrame = { type: 'state' } & State
export interface StatusEvent {
  type: 'status'
  ws: string
  folder: string
  status: AgentStatus
  exit_code?: number // only with "stopped"
}
export interface MessageChangeEvent {
  type: 'message'
  ws: string
  folder: string // the target folder
  message: Message
}
export interface AccountEvent {
  type: 'account'
  account: Account
}
// The outside-terminal check changed for a folder (#89); the store refreshes.
export interface ExternalEvent {
  type: 'external'
  ws: string
  folder: string
}
export type DaemonEvent = StatusEvent | MessageChangeEvent | AccountEvent | ExternalEvent
export type EventFrame = StateFrame | DaemonEvent
