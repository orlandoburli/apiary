import { analyzeApiary } from './documentModel';

export interface ApiaryConfig {
  version?: string;
  runners?: Runner[];
  default_runner?: string;
  sources?: Source[];
  agents?: Agent[];
  workflows?: Workflow[];
  settings?: Settings;
}

export interface Runner {
  id: string;
  type: string;
  provider?: string;
  models?: string[];
}

export interface Source {
  id: string;
  type: string;
  config?: { repo?: string; workspace?: string; project?: string; api_key?: string };
  poll_interval?: string;
  filters?: { states?: string[]; labels?: string[] };
}

export interface Agent {
  id: string;
  description?: string;
  soul_file?: string;
  model?: string;
  runner?: string;
  max_workers?: number;
  skills?: string[];
}

export interface Workflow {
  id: string;
  description?: string;
  resume?: string;
  trigger?: Trigger;
  steps?: Step[];
  on_complete?: OnComplete;
  on_fail?: OnFail;
}

export interface Trigger {
  priority?: number;
  match?: TriggerMatch;
}

export interface TriggerMatch {
  source?: string;
  labels?: string[];
  exclude_label_prefix?: string;
  types?: string[];
  states?: string[];
}

export interface Step {
  id: string;
  type?: string;
  agent?: string;
  if?: string;
  prompt?: string;
  summary_prompt?: string;
  output_schema?: unknown;
  output?: unknown;
  memory?: { write?: string[]; read?: string[] };
  branches?: Branch[];
  reject_when?: string;
  on_reject?: { restart_from?: string; max?: number };
  timeout?: string;
  message?: string;
  resume_on?: Record<string, string>;
  abort_on?: Record<string, string>;
	wait_for?: { kind?: string; check_interval?: string; max_duration?: string; fail_if_not_passed?: boolean; remove_label?: string; satisfied_when?: string[]; blocker_link_type?: string; on_timeout?: string };
	workflow?: string;
	uses?: string;
	with?: Record<string, unknown>;
}

export interface Branch {
  if?: string;
  else?: boolean;
  goto: string;
}

export interface OnComplete {
  set_state?: string;
  add_labels?: string[];
}

export interface OnFail {
  add_labels?: string[];
}

export interface Settings {
  concurrency?: number;
  log_level?: string;
  state_lock?: boolean;
  result_comment?: boolean;
}

export function parseApiary(content: string): ApiaryConfig {
  return analyzeApiary(content).config;
}
