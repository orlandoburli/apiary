import * as yaml from 'js-yaml';

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
  result_comment?: string;
  inputs?: Record<string, WorkflowInput>;
  outputs?: Record<string, WorkflowOutput>;
  trigger?: Trigger;
  steps?: Step[];
  on_complete?: OnComplete;
  on_fail?: OnFail;
  env?: Record<string, string>;
}

export interface WorkflowInput {
  type: string;
  required?: boolean;
  default?: unknown;
}

export interface WorkflowOutput {
  type: string;
  value: string;
}

export interface Trigger {
  priority?: number;
  exclusive?: boolean;
  once?: boolean;
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
  name?: string;

  // --- agent step ---
  agent?: string;
  model?: string;
  prompt?: string;
  summary_prompt?: string;
  idempotent?: boolean;
  output_schema?: OutputSchema;
  output?: OutputSchema;
  on_missing_output?: string;
  memory?: MemoryConfig;
  on_pass?: { next?: string };
  on_fail?: StepOutcome;
  on_conflict?: StepOutcome;
  publish?: string;
  spawn?: string;
  materialize?: string;
  env?: Record<string, string>;
  action_class?: string;
  condition?: string;
  fail_when?: string;
  seq_depends_on?: string[];

  // --- split step ---
  multi?: boolean;
  branches?: Branch[];

  // --- approval step ---
  message?: string;
  resume_on?: Record<string, string>;
  abort_on?: Record<string, string>;
  timeout?: string;
  approvers?: string[];
  required_approvals?: number;
  fields?: ApprovalField[];
  remind_after?: string;
  escalate_after?: string;
  escalate_to?: string[];
  delegates?: Record<string, string[]>;

  // --- wait_for step ---
  wait_for?: WaitForConfig;

  // --- foreach step ---
  items?: string;
  as?: string;
  concurrency?: number;
  max_items?: number;
  fail_fast?: boolean;
  step?: Step;

  // --- sub-workflow step ---
  workflow?: string;
  uses?: string;
  with?: Record<string, unknown>;

  // --- v2 authored fields (present before lowering) ---
  if?: string;
  reject_when?: string;
  on_reject?: OnRejectConfig;
  steps?: Step[];       // SubSteps: v2 sequential sub-group
  parallel?: Step[];    // ParallelSteps: v2 parallel group
  join?: string;
  for_each?: string;
  max?: number;
}

export interface OutputSchema {
  type: string;
  properties?: Record<string, SchemaField>;
  required?: string[];
}

export interface SchemaField {
  type: string;
  enum?: string[];
  items?: SchemaField;
  properties?: Record<string, SchemaField>;
  required?: string[];
}

export interface MemoryConfig {
  read?: boolean;
  write?: string[];
  recall?: string[];
  memorize?: string;
}

export interface StepOutcome {
  goto?: string;
  max_retries?: number;
}

export interface OnRejectConfig {
  restart_from: string;
  max?: number;
}

export interface Branch {
  if?: string;
  else?: boolean;
  goto: string;
}

export interface ApprovalField {
  name: string;
  label?: string;
  type?: string;
  required?: boolean;
  options?: string[];
}

export interface WaitForConfig {
  kind?: string;
  check_interval?: string;
  max_duration?: string;
  fail_if_not_passed?: boolean;
  remove_label?: string;
  satisfied_when?: string[];
  blocker_link_type?: string;
  on_timeout?: string;
}

export interface OnComplete {
  set_state?: string;
  add_labels?: string[];
  remove_labels?: string[];
}

export interface OnFail {
  add_labels?: string[];
  remove_labels?: string[];
}

export interface Settings {
  concurrency?: number;
  log_level?: string;
  state_lock?: boolean;
  result_comment?: boolean;
}

export function parseApiary(content: string): ApiaryConfig {
  const doc = yaml.load(content) as ApiaryConfig;
  if (!doc || typeof doc !== 'object') {
    throw new Error('Invalid apiary YAML: expected an object at the root level');
  }
  return doc;
}

// Classify a step's editability in the visual editor.
// 'editable'   — fully supported; the editor can display and save changes.
// 'readonly'   — uses v2 sub-group constructs we can display but not yet serialize back.
// 'unsupported'— unknown step type; shown with a lock icon, never modified.
export type StepEditability = 'editable' | 'readonly' | 'unsupported';

const SUPPORTED_TYPES = new Set(['agent', 'split', 'approval', 'foreach', 'workflow', 'wait_for', '']);

// V2 sub-group fields that the serializer cannot safely reconstruct.
const V2_AUTHORED_SUBGROUP_FIELDS: (keyof Step)[] = ['steps', 'parallel', 'for_each'];

export function classifyStepEditability(step: Step): StepEditability {
  if (!SUPPORTED_TYPES.has(step.type ?? '')) { return 'unsupported'; }
  for (const field of V2_AUTHORED_SUBGROUP_FIELDS) {
    if (step[field] !== undefined) { return 'readonly'; }
  }
  return 'editable';
}

// Collect validation errors for a config model. Returns a map keyed by step id
// or a workflow-level key. Used by the editor to annotate nodes with errors.
export interface ValidationError {
  scope: 'workflow' | 'step';
  stepId?: string;
  message: string;
}

export function validateConfig(config: ApiaryConfig): ValidationError[] {
  const errors: ValidationError[] = [];
  const agentIds = new Set((config.agents ?? []).map(a => a.id));
  const sourceIds = new Set((config.sources ?? []).map(s => s.id));
  const runnerIds = new Set((config.runners ?? []).map(r => r.id));

  if (!config.version) {
    errors.push({ scope: 'workflow', message: 'version is required' });
  }

  for (const wf of config.workflows ?? []) {
    if (!wf.id) {
      errors.push({ scope: 'workflow', message: 'workflow is missing an id' });
    }
    if (wf.trigger?.match?.source && !sourceIds.has(wf.trigger.match.source)) {
      errors.push({ scope: 'workflow', stepId: wf.id, message: `trigger references unknown source "${wf.trigger.match.source}"` });
    }

    const stepIds = new Set((wf.steps ?? []).map(s => s.id));

    for (const step of wf.steps ?? []) {
      if (!step.id) {
        errors.push({ scope: 'step', message: 'a step is missing an id' });
        continue;
      }
      // Agent reference check
      if (step.agent && !agentIds.has(step.agent)) {
        errors.push({ scope: 'step', stepId: step.id, message: `references unknown agent "${step.agent}"` });
      }
      // Runner override check
      if (step.model && config.runners && runnerIds.size > 0 && !config.default_runner) {
        // just a soft warning; runners must be validated server-side
      }
      // Split branch targets
      for (const branch of step.branches ?? []) {
        if (!stepIds.has(branch.goto)) {
          errors.push({ scope: 'step', stepId: step.id, message: `branch goto "${branch.goto}" references unknown step` });
        }
      }
      // on_fail / on_pass targets
      if (step.on_fail?.goto && !stepIds.has(step.on_fail.goto)) {
        errors.push({ scope: 'step', stepId: step.id, message: `on_fail.goto "${step.on_fail.goto}" references unknown step` });
      }
      if (step.on_pass?.next && !stepIds.has(step.on_pass.next)) {
        errors.push({ scope: 'step', stepId: step.id, message: `on_pass.next "${step.on_pass.next}" references unknown step` });
      }
      // on_reject target
      if (step.on_reject?.restart_from && !stepIds.has(step.on_reject.restart_from)) {
        errors.push({ scope: 'step', stepId: step.id, message: `on_reject.restart_from "${step.on_reject.restart_from}" references unknown step` });
      }
      // foreach step ref
      if (step.step?.agent && !agentIds.has(step.step.agent)) {
        errors.push({ scope: 'step', stepId: step.id, message: `foreach child references unknown agent "${step.step.agent}"` });
      }
      // sub-workflow reference
      if (step.workflow) {
        const wfIds = new Set((config.workflows ?? []).map(w => w.id));
        if (!wfIds.has(step.workflow)) {
          errors.push({ scope: 'step', stepId: step.id, message: `references unknown workflow "${step.workflow}"` });
        }
      }
    }
  }

  return errors;
}
