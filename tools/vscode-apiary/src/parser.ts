import * as yaml from 'js-yaml';

export interface ApiaryConfig {
  version?: string;
  runners?: Runner[];
  default_runner?: string;
  sources?: Source[];
  agents?: Agent[];
  workflows?: Workflow[];
  settings?: Settings;
  workers?: Worker[];
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

export interface Worker {
  id: string;
  runner?: string;
  model?: string;
}

export interface Workflow {
  id: string;
  description?: string;
  resume?: string;
  result_comment?: string;
  inputs?: Record<string, WorkflowInput>;
  outputs?: Record<string, WorkflowOutput>;
  trigger?: Trigger;
  steps?: Step[];
  on_complete?: OnComplete;
  on_fail?: OnComplete;
  env?: Record<string, string>;
}

export interface WorkflowInput {
  description?: string;
  required?: boolean;
  default?: unknown;
}

export interface WorkflowOutput {
  description?: string;
  value?: string;
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

  // Agent step fields
  agent?: string;
  prompt?: string;
  summary_prompt?: string;
  output_schema?: unknown;
  output?: unknown;
  memory?: { write?: string[]; read?: string[] };
  model?: string;
  publish?: string;
  spawn?: string;
  materialize?: string;
  action_class?: string;
  on_pass?: OnComplete;
  on_fail?: StepOnFail;
  on_conflict?: unknown;
  env?: Record<string, string>;
  on_missing_output?: string;

  // Conditions / v2 authoring
  if?: string;
  reject_when?: string;
  on_reject?: { restart_from?: string; max?: number };

  // Split step
  branches?: Branch[];

  // Approval step
  message?: string;
  timeout?: string;
  resume_on?: Record<string, string>;
  abort_on?: Record<string, string>;
  approvers?: string[];
  required_approvals?: number;
  approval_fields?: unknown[];
  remind_after?: string;
  escalate_after?: string;
  escalate_to?: string[];
  delegates?: unknown[];

  // Foreach step
  items?: string;
  as?: string;
  concurrency?: number;
  max_items?: number;
  fail_fast?: boolean;
  step?: Step;

  // Workflow step (sub-workflow reference)
  workflow?: string;
  uses?: string;
  with?: Record<string, unknown>;

  // Wait-for step
  wait_for?: WaitForConfig;

  // Parallel step (v2 lowering or authored)
  sub_steps?: Step[];
  join?: string;
  max?: number;

  // V1 DAG syntax — not editable in the visual editor
  depends_on?: string[];
  seq_depends_on?: string[];
}

export interface WaitForConfig {
  kind?: string;
  run?: string;
  conclusion?: string[];
  timeout?: string;
  poll_interval?: string;
  task?: string;
  workflow?: string;
  states?: string[];
}

export interface Branch {
  if?: string;
  else?: boolean;
  goto: string;
}

export interface StepOnFail {
  goto?: string;
  add_labels?: string[];
}

export interface OnComplete {
  set_state?: string;
  add_labels?: string[];
  remove_labels?: string[];
}

export interface Settings {
  concurrency?: number;
  log_level?: string;
  state_lock?: boolean;
  result_comment?: boolean | string;
}

// Step types the visual editor can render and edit
export const EDITABLE_STEP_TYPES = new Set([
  'agent', 'split', 'approval', 'foreach', 'workflow', 'wait_for', 'parallel',
]);

// Step fields preserved during round-trip but not surfaced in the form UI
const PASSTHROUGH_FIELDS: (keyof Step)[] = [
  'env', 'memory', 'output_schema', 'spawn', 'materialize', 'action_class',
  'on_conflict', 'approval_fields', 'delegates', 'on_missing_output',
];

export interface StepEditability {
  /** True when the editor fully represents this step's editable fields in the form. */
  fullyEditable: boolean;
  /** True when the step cannot be edited at all (unsupported type or v1 DAG syntax). */
  readOnly: boolean;
  /** Fields present on this step that are preserved through round-trip but hidden from the form. */
  passthroughFields: string[];
}

export function classifyStep(step: Step): StepEditability {
  const type = step.type ?? 'agent';
  if (!EDITABLE_STEP_TYPES.has(type)) {
    return { fullyEditable: false, readOnly: true, passthroughFields: [] };
  }
  const usesV1Dag =
    (step.depends_on?.length ?? 0) > 0 ||
    (step.seq_depends_on?.length ?? 0) > 0;
  if (usesV1Dag) {
    return { fullyEditable: false, readOnly: true, passthroughFields: [] };
  }
  const stepRecord = step as unknown as Record<string, unknown>;
  const passthrough = PASSTHROUGH_FIELDS.filter(f => stepRecord[f] !== undefined);
  return {
    fullyEditable: passthrough.length === 0,
    readOnly: false,
    passthroughFields: passthrough,
  };
}

export function parseApiary(content: string): ApiaryConfig {
  const doc = yaml.load(content) as ApiaryConfig;
  if (!doc || typeof doc !== 'object' || Array.isArray(doc)) {
    throw new Error('Invalid apiary YAML: expected an object at the root level');
  }
  return doc;
}
