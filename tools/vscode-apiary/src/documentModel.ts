import Ajv, { ErrorObject } from 'ajv';
import { Document, isAlias, isMap, isSeq, LineCounter, Node, parseDocument } from 'yaml';
import type { ApiaryConfig, Step, Workflow } from './parser';

const apiarySchema = require('../../../schema/apiary.json') as object;
const ajv = new Ajv({ allErrors: true, strict: false, allowUnionTypes: true, validateFormats: false });
const validateSchema = ajv.compile(apiarySchema);

export type Path = Array<string | number>;
export interface Diagnostic {
  message: string;
  path: Path;
  line: number;
  column: number;
  workflowId?: string;
  stepId?: string;
  severity: 'error' | 'warning';
}
export interface WorkflowSupport { id: string; editable: boolean; reasons: string[]; }
export interface SemanticChange { kind: 'add' | 'remove' | 'change'; path: string; before?: unknown; after?: unknown; }
export interface DocumentAnalysis {
  config: ApiaryConfig;
  text: string;
  support: WorkflowSupport[];
  diagnostics: Diagnostic[];
  formSchema: Record<string, Record<string, { type?: string; description?: string; enum?: string[] }>>;
}
export type WorkflowEdit =
  | { kind: 'set'; workflowId: string; stepId?: string; path: Path; value: unknown }
  | { kind: 'delete'; workflowId: string; stepId?: string; path: Path }
  | { kind: 'addStep'; workflowId: string; stepType: 'agent' | 'split' | 'approval' | 'wait_for' | 'workflow'; id: string }
  | { kind: 'deleteStep'; workflowId: string; stepId: string }
  | { kind: 'moveStep'; workflowId: string; stepId: string; offset: -1 | 1 }
  | { kind: 'addBranch'; workflowId: string; stepId: string }
  | { kind: 'deleteBranch'; workflowId: string; stepId: string; branchIndex: number };

const workflowKeys = new Set(['id', 'description', 'inputs', 'outputs', 'resume', 'result_comment', 'trigger', 'steps', 'on_complete', 'on_fail', 'env']);
const triggerKeys = new Set(['priority', 'exclusive', 'once', 'match']);
const matchKeys = new Set(['source', 'states', 'labels', 'exclude_labels', 'exclude_label_prefix', 'types', 'priority', 'title_regex']);
const commonStepKeys = new Set(['id', 'type', 'name', 'if', 'condition', 'on_pass', 'on_fail', 'on_conflict']);
const stepKeys: Record<string, Set<string>> = {
  agent: new Set([...commonStepKeys, 'agent', 'model', 'prompt', 'summary_prompt', 'idempotent', 'output_schema', 'output', 'on_missing_output', 'memory', 'reject_when', 'on_reject', 'fail_when', 'publish', 'spawn', 'materialize', 'env', 'action_class']),
  split: new Set([...commonStepKeys, 'multi', 'branches']),
  approval: new Set([...commonStepKeys, 'message', 'resume_on', 'abort_on', 'timeout', 'approvers', 'required_approvals', 'fields', 'remind_after', 'escalate_after', 'escalate_to', 'delegates', 'action_class']),
  wait_for: new Set([...commonStepKeys, 'wait_for']),
  workflow: new Set([...commonStepKeys, 'workflow', 'uses', 'with']),
};

export function analyzeApiary(content: string): DocumentAnalysis {
  const lineCounter = new LineCounter();
  const document = parseDocument(content, { lineCounter, keepSourceTokens: true, prettyErrors: true });
  if (document.errors.length) { throw document.errors[0]; }
  const config = document.toJS() as ApiaryConfig;
  if (!config || typeof config !== 'object') { throw new Error('Invalid apiary YAML: expected an object at the root level'); }
  const schema = apiarySchema as { definitions?: Record<string, { properties?: Record<string, { type?: string; description?: string; enum?: string[] }> }> };
  return {
    config, text: content, support: analyzeSupport(config, document), diagnostics: diagnostics(config, document, lineCounter),
    formSchema: {
      step: schema.definitions?.StepConfig?.properties ?? {},
      wait_for: schema.definitions?.WaitForConfig?.properties ?? {},
      match: schema.definitions?.RouteMatch?.properties ?? {},
      approval_trigger: schema.definitions?.ApprovalTrigger?.properties ?? {},
    },
  };
}

export function applyWorkflowEdit(content: string, edit: WorkflowEdit): { analysis: DocumentAnalysis; changes: SemanticChange[] } {
  const before = analyzeApiary(content);
  const supported = before.support.find(item => item.id === edit.workflowId);
  if (!supported?.editable) { throw new Error(`Workflow ${edit.workflowId} is read-only: ${supported?.reasons.join('; ') || 'not found'}`); }
  const document = parseDocument(content, { keepSourceTokens: true });
  const workflowIndex = (before.config.workflows ?? []).findIndex(workflow => workflow.id === edit.workflowId);
  if (workflowIndex < 0) { throw new Error(`Workflow ${edit.workflowId} not found`); }
  const base: Path = ['workflows', workflowIndex];
  const steps = before.config.workflows?.[workflowIndex].steps ?? [];
  const stepIndex = 'stepId' in edit && edit.stepId ? steps.findIndex(step => step.id === edit.stepId) : -1;
  if ('stepId' in edit && edit.stepId && stepIndex < 0) { throw new Error(`Step ${edit.stepId} not found`); }
  switch (edit.kind) {
    case 'set': document.setIn([...base, ...(edit.stepId ? ['steps', stepIndex] : []), ...edit.path], edit.value); break;
    case 'delete': document.deleteIn([...base, ...(edit.stepId ? ['steps', stepIndex] : []), ...edit.path]); break;
    case 'addStep': {
      const seq = document.getIn([...base, 'steps'], true);
      if (!isSeq(seq)) { throw new Error(`Workflow ${edit.workflowId} has no editable steps sequence`); }
      seq.add(defaultStep(edit.stepType, edit.id));
      break;
    }
    case 'deleteStep': {
      const seq = document.getIn([...base, 'steps'], true);
      if (!isSeq(seq)) { throw new Error('Steps are not a YAML sequence'); }
      seq.items.splice(stepIndex, 1); break;
    }
    case 'moveStep': {
      const seq = document.getIn([...base, 'steps'], true);
      if (!isSeq(seq)) { throw new Error('Steps are not a YAML sequence'); }
      const target = stepIndex + edit.offset;
      if (target < 0 || target >= seq.items.length) { break; }
      const [node] = seq.items.splice(stepIndex, 1); seq.items.splice(target, 0, node); break;
    }
    case 'addBranch': {
      const seq = document.getIn([...base, 'steps', stepIndex, 'branches'], true);
      if (!isSeq(seq)) { throw new Error(`Step ${edit.stepId} has no editable branches sequence`); }
      seq.add({ if: '', goto: '' }); break;
    }
    case 'deleteBranch': {
      const seq = document.getIn([...base, 'steps', stepIndex, 'branches'], true);
      if (!isSeq(seq)) { throw new Error(`Step ${edit.stepId} has no editable branches sequence`); }
      if (edit.branchIndex >= 0 && edit.branchIndex < seq.items.length) { seq.items.splice(edit.branchIndex, 1); }
      break;
    }
  }
  const candidate = document.toString({ lineWidth: 0 });
  const analysis = analyzeApiary(candidate);
  return { analysis, changes: semanticDiff(before.config, analysis.config) };
}

function defaultStep(type: string, id: string): Step {
  if (type === 'agent') { return { id, agent: '' }; }
  if (type === 'split') { return { id, type, branches: [{ else: true, goto: '' }] }; }
  if (type === 'approval') { return { id, type, message: '', resume_on: {} }; }
  if (type === 'wait_for') { return { id, type, wait_for: { kind: 'ci' } } as Step; }
  return { id, type, workflow: '' } as Step;
}

function analyzeSupport(config: ApiaryConfig, document: Document): WorkflowSupport[] {
  return (config.workflows ?? []).map((workflow, index) => {
    const reasons: string[] = [];
    unknownKeys(workflow as unknown as Record<string, unknown>, workflowKeys, 'workflow', reasons);
    const raw = document.getIn(['workflows', index], true) as Node | undefined;
    if (containsAlias(raw)) { reasons.push('YAML aliases are preserved but not visually editable'); }
    if (workflow.trigger) {
      unknownKeys(workflow.trigger as unknown as Record<string, unknown>, triggerKeys, 'trigger', reasons);
      if (workflow.trigger.match) { unknownKeys(workflow.trigger.match as unknown as Record<string, unknown>, matchKeys, 'trigger.match', reasons); }
    }
    for (const step of workflow.steps ?? []) {
      const type = step.type || 'agent';
      const allowed = stepKeys[type];
      if (!allowed) { reasons.push(`step ${step.id}: unsupported type ${type}`); continue; }
      unknownKeys(step as unknown as Record<string, unknown>, allowed, `step ${step.id}`, reasons);
    }
    return { id: workflow.id, editable: reasons.length === 0, reasons };
  });
}

function unknownKeys(value: Record<string, unknown>, allowed: Set<string>, label: string, reasons: string[]): void {
  for (const key of Object.keys(value)) { if (!allowed.has(key)) { reasons.push(`${label}: unsupported key ${key}`); } }
}
function containsAlias(node: Node | undefined): boolean {
  if (!node) { return false; }
  if (isAlias(node)) { return true; }
  if (isMap(node)) { return node.items.some(pair => containsAlias(pair.key as Node) || containsAlias(pair.value as Node)); }
  if (isSeq(node)) { return node.items.some(item => containsAlias(item as Node)); }
  return false;
}

function diagnostics(config: ApiaryConfig, document: Document, lines: LineCounter): Diagnostic[] {
  const result: Diagnostic[] = [];
  if (!validateSchema(config)) {
    for (const error of validateSchema.errors ?? []) { result.push(schemaDiagnostic(error, config, document, lines)); }
  }
  const sources = new Set((config.sources ?? []).map(item => item.id));
  const runners = new Set((config.runners ?? []).map(item => item.id));
  const agents = new Set((config.agents ?? []).map(item => item.id));
  const workflows = new Set((config.workflows ?? []).map(item => item.id));
  (config.agents ?? []).forEach((agent, index) => { if (agent.runner && !runners.has(agent.runner)) { result.push(atPath(document, lines, ['agents', index, 'runner'], `Unknown runner ${agent.runner}`)); } });
  (config.workflows ?? []).forEach((workflow, wi) => {
    const ids = new Set((workflow.steps ?? []).map(step => step.id));
    if (workflow.trigger?.match?.source && !sources.has(workflow.trigger.match.source)) { result.push(atPath(document, lines, ['workflows', wi, 'trigger', 'match', 'source'], `Unknown source ${workflow.trigger.match.source}`, workflow.id)); }
    (workflow.steps ?? []).forEach((step, si) => {
      const base: Path = ['workflows', wi, 'steps', si];
      if (step.agent && !agents.has(step.agent)) { result.push(atPath(document, lines, [...base, 'agent'], `Unknown agent ${step.agent}`, workflow.id, step.id)); }
      if (step.workflow && !workflows.has(step.workflow)) { result.push(atPath(document, lines, [...base, 'workflow'], `Unknown workflow ${step.workflow}`, workflow.id, step.id)); }
      for (const [path, target] of stepTargets(step)) { if (target && !ids.has(target)) { result.push(atPath(document, lines, [...base, ...path], `Unknown step ${target}`, workflow.id, step.id)); } }
      for (const [path, expression] of stepExpressions(step)) {
        if (!validExpressionShape(expression)) { result.push(atPath(document, lines, [...base, ...path], 'Malformed expression: unbalanced quotes, brackets, or ${{ }} wrapper', workflow.id, step.id)); }
      }
    });
  });
  return result;
}

function stepExpressions(step: Step): Array<[Path, string]> {
  const values: Array<[Path, string]> = [];
  if (step.if) { values.push([['if'], step.if]); }
  if (step.reject_when) { values.push([['reject_when'], step.reject_when]); }
  (step.branches ?? []).forEach((branch, index) => { if (branch.if) { values.push([['branches', index, 'if'], branch.if]); } });
  return values;
}

function validExpressionShape(expression: string): boolean {
  const trimmed = expression.trim();
  if (!trimmed) { return false; }
  if ((trimmed.startsWith('${{') && !trimmed.endsWith('}}')) || (!trimmed.startsWith('${{') && trimmed.endsWith('}}'))) { return false; }
  const stack: string[] = [];
  let quote = '';
  for (let index = 0; index < trimmed.length; index++) {
    const char = trimmed[index];
    if (quote) { if (char === quote && trimmed[index - 1] !== '\\') { quote = ''; } continue; }
    if (char === '"' || char === "'") { quote = char; continue; }
    if ('([{'.includes(char)) { stack.push(char); continue; }
    if (')]}'.includes(char)) { const expected = char === ')' ? '(' : char === ']' ? '[' : '{'; if (stack.pop() !== expected) { return false; } }
  }
  return !quote && stack.length === 0;
}

function stepTargets(step: Step): Array<[Path, string]> {
  const targets: Array<[Path, string]> = [];
  (step.branches ?? []).forEach((branch, i) => targets.push([['branches', i, 'goto'], branch.goto]));
  if (step.on_reject?.restart_from) { targets.push([['on_reject', 'restart_from'], step.on_reject.restart_from]); }
  return targets;
}

function schemaDiagnostic(error: ErrorObject, config: ApiaryConfig, document: Document, lines: LineCounter): Diagnostic {
  const path = error.instancePath.split('/').slice(1).map(segment => /^\d+$/.test(segment) ? Number(segment) : segment.replace(/~1/g, '/').replace(/~0/g, '~'));
  const wfIndex = path[0] === 'workflows' && typeof path[1] === 'number' ? path[1] : -1;
  const stepIndex = wfIndex >= 0 && path[2] === 'steps' && typeof path[3] === 'number' ? path[3] : -1;
  return atPath(document, lines, path, `${error.instancePath || '/'} ${error.message ?? 'is invalid'}`, config.workflows?.[wfIndex]?.id, config.workflows?.[wfIndex]?.steps?.[stepIndex]?.id);
}
function atPath(document: Document, lines: LineCounter, path: Path, message: string, workflowId?: string, stepId?: string): Diagnostic {
  let node = document.getIn(path, true) as Node | undefined;
  while (!node && path.length) { path = path.slice(0, -1); node = document.getIn(path, true) as Node | undefined; }
  const position = lines.linePos(node?.range?.[0] ?? 0);
  return { message, path, line: position.line, column: position.col, workflowId, stepId, severity: 'error' };
}

export function semanticDiff(before: unknown, after: unknown, path = '$'): SemanticChange[] {
  if (Object.is(before, after)) { return []; }
  if (Array.isArray(before) && Array.isArray(after)) {
    const result: SemanticChange[] = [];
    for (let i = 0; i < Math.max(before.length, after.length); i++) { result.push(...semanticDiff(before[i], after[i], `${path}[${i}]`)); }
    return result;
  }
  if (plainObject(before) && plainObject(after)) {
    const result: SemanticChange[] = [];
    for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) { result.push(...semanticDiff(before[key], after[key], `${path}.${key}`)); }
    return result;
  }
  const kind = before === undefined ? 'add' : after === undefined ? 'remove' : 'change';
  return [{ kind, path, before, after }];
}
function plainObject(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
