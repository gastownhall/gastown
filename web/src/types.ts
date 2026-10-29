// Shapes of the gtb server's browser API (internal/server/webapi.go).

export interface ArtifactView {
  name: string;
  sha: string;
  content: string;
}

export interface FileStat {
  path: string;
  added: number;
  deleted: number;
}

export interface Usage {
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
}

export interface ItemRow {
  id: string;
  rig: string;
  kind: string;
  step: string;
  title: string;
  parent: string;
  branch: string;
  waiting_for_human: string;
  depends_on: string[] | null;
  step_entered_at: string;
  usage?: Usage;
  machine_id: string;
  updated_at: string;
  last_seen_at?: string;
  freshness_seconds: number;
  stale: boolean;
}

export interface Lane {
  step: string;
  items: ItemRow[];
}

export interface Board {
  lanes: Lane[];
  needs_you: ItemRow[];
}

export interface ItemEvent {
  id: string;
  at: string;
  type: string;
  item_id: string;
  data: unknown;
}

export interface ItemDetail {
  item: ItemRow;
  artifacts: ArtifactView[];
  events: ItemEvent[];
  diffstat: FileStat[];
}

export interface Rig {
  name: string;
  remote: string;
  gated: boolean;
}

export interface Machine {
  id: string;
  hostname: string;
  os: string;
  arch: string;
  gtb_version: string;
  protocol: string;
  created_at: string;
  last_seen_at: string;
  revoked_at: string | null;
  freshness_seconds: number;
  stale: boolean;
  rigs: Rig[] | null;
}

export interface Me {
  login: string;
  csrf_token: string;
}

export interface Accepted {
  key: string;
  status: string;
}

export interface RequestResult {
  ok: boolean;
  error: string;
  data?: unknown;
}

export interface RequestStatus {
  status: "pending" | "delivered" | "done" | "expired";
  result: RequestResult | null;
}

export interface EnrollCode {
  code: string;
  expires_at: string;
}
