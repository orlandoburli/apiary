import { ApiaryConfig, Agent, Source, Workflow, Step, Branch, Trigger, classifyStep } from './parser';
import { ValidationError } from './validator';

// Produce a valid Mermaid node ID (alphanumeric + underscore only)
export function sid(s: string): string {
  return 'N_' + s.replace(/[^a-zA-Z0-9]/g, '_');
}

// Escape text for Mermaid node labels (no raw double-quotes)
function esc(s: string): string {
  return s.replace(/"/g, '#quot;');
}

function shortModel(model: string): string {
  return model.replace(/^claude-/, '');
}

function buildTriggerLabel(trigger?: Trigger): string {
  if (!trigger?.match) { return ''; }
  const m = trigger.match;
  const parts: string[] = [];
  if (m.labels?.length) { parts.push(m.labels.join(', ')); }
  if (m.exclude_label_prefix) { parts.push(`excl: ${m.exclude_label_prefix}`); }
  if (m.types?.length) { parts.push(`types: ${m.types.join(', ')}`); }
  if (m.states?.length) { parts.push(`states: ${m.states.join(', ')}`); }
  return parts.join(' · ');
}

function buildBranchLabel(branch: Branch): string {
  if (branch.else) { return 'else'; }
  if (!branch.if) { return ''; }
  return branch.if
    .replace(/memory\./g, '')
    .replace(/ == /g, ' = ')
    .replace(/["']/g, '');
}

function sourceDisplay(source: Source): string {
  const repo = source.config?.repo ?? source.config?.workspace ?? source.id;
  return `${esc(source.id)}\\n${esc(repo)}`;
}

export interface DiagramResult {
  title: string;
  diagram: string;
}

export interface RenderOptions {
  /** When true, adds Mermaid click directives so nodes are interactive. */
  interactive?: boolean;
  /** Node IDs with validation errors (highlighted with red border). */
  errorNodeIds?: Set<string>;
  /** Node IDs that are read-only (dimmed). */
  readOnlyNodeIds?: Set<string>;
}

export function renderApiary(
  config: ApiaryConfig,
  options: RenderOptions = {},
): DiagramResult[] {
  const agentMap = new Map<string, Agent>(
    (config.agents ?? []).map(a => [a.id, a]),
  );
  const sourceMap = new Map<string, Source>(
    (config.sources ?? []).map(s => [s.id, s]),
  );

  return (config.workflows ?? []).map(wf => ({
    title: wf.id + (wf.description ? ` — ${wf.description}` : ''),
    diagram: renderWorkflow(wf, agentMap, sourceMap, options),
  }));
}

/**
 * Build error/read-only node ID sets from a list of ValidationErrors.
 */
export function buildErrorSets(errors: ValidationError[]): {
  errorNodeIds: Set<string>;
  readOnlyNodeIds: Set<string>;
} {
  const errorNodeIds = new Set<string>();
  const readOnlyNodeIds = new Set<string>();
  for (const e of errors) {
    if (!e.nodeId) { continue; }
    if (e.severity === 'error') {
      errorNodeIds.add(e.nodeId);
    } else {
      readOnlyNodeIds.add(e.nodeId);
    }
  }
  return { errorNodeIds, readOnlyNodeIds };
}

function renderWorkflow(
  wf: Workflow,
  agentMap: Map<string, Agent>,
  sourceMap: Map<string, Source>,
  options: RenderOptions,
): string {
  const steps = wf.steps ?? [];
  const { interactive, errorNodeIds, readOnlyNodeIds } = options;

  const stepId = (step: Step) => sid(`${wf.id}_${step.id}`);
  const stepIdByName = (name: string) => sid(`${wf.id}_${name}`);

  const lines: string[] = [
    'flowchart TD',
    '  classDef agentNode fill:#1a3a6b,stroke:#5b9bd5,color:#dce8f8,stroke-width:1px',
    '  classDef splitNode fill:#3a1a6b,stroke:#9b5bd5,color:#e8dcf8,stroke-width:1px',
    '  classDef approvalNode fill:#5a4a0a,stroke:#c8a83a,color:#f8f0dc,stroke-width:1px',
    '  classDef sourceNode fill:#0a3a1a,stroke:#3aaa6a,color:#dcf8e8,stroke-width:1px',
    '  classDef foreachNode fill:#1a3a3a,stroke:#3aaaaa,color:#dcf8f8,stroke-width:1px',
    '  classDef waitNode fill:#3a2a0a,stroke:#aa7a3a,color:#f8f0dc,stroke-width:1px',
    '  classDef workflowNode fill:#1a1a3a,stroke:#5b5bd5,color:#dcdcf8,stroke-width:2px',
    '  classDef parallelNode fill:#0a2a1a,stroke:#3a8a5a,color:#dcf8dc,stroke-width:1px',
    '  classDef errorNode stroke:#be1100,stroke-width:3px',
    '  classDef readOnlyNode opacity:0.55',
  ];

  // Source node
  const sourceKey = wf.trigger?.match?.source;
  const source = sourceKey ? sourceMap.get(sourceKey) : undefined;
  const srcId = sourceKey ? sid('SRC_' + sourceKey) : null;

  if (srcId && source) {
    lines.push(`  ${srcId}["📦 ${sourceDisplay(source)}"]:::sourceNode`);
  } else if (srcId) {
    lines.push(`  ${srcId}["📦 ${esc(sourceKey!)}"]:::sourceNode`);
  }

  if (srcId && steps.length > 0) {
    const firstId = stepId(steps[0]);
    const lbl = buildTriggerLabel(wf.trigger);
    const edge = lbl ? `-->|"${esc(lbl)}"| ` : '--> ';
    lines.push(`  ${srcId} ${edge}${firstId}`);
  }

  const wfLabel = esc(wf.id + (wf.description ? ` — ${wf.description}` : ''));
  lines.push(`  subgraph ${sid('WF_' + wf.id)}["${wfLabel}"]`);

  // Node declarations
  for (const step of steps) {
    const id = stepId(step);
    const editability = classifyStep(step);
    const type = step.type ?? 'agent';

    const base = nodeClass(type);
    const extra: string[] = [base];
    if (errorNodeIds?.has(id)) { extra.push('errorNode'); }
    if (readOnlyNodeIds?.has(id) || editability.readOnly) { extra.push('readOnlyNode'); }
    const cls = extra.join(',');

    switch (type) {
      case 'split':
        lines.push(`    ${id}{"${esc(step.id)}\\nsplit"}:::${cls}`);
        break;

      case 'approval':
        lines.push(`    ${id}[/"⏳ ${esc(step.id)}\\napproval"/]:::${cls}`);
        break;

      case 'foreach': {
        const itemsStr = step.items ? `\\n${esc(step.items)}` : '';
        lines.push(`    ${id}["🔁 ${esc(step.id)}${itemsStr}"]:::${cls}`);
        break;
      }

      case 'workflow': {
        const ref = step.workflow ?? step.uses ?? '?';
        lines.push(`    ${id}[["📂 ${esc(step.id)}\\n${esc(ref)}"]]:::${cls}`);
        break;
      }

      case 'wait_for': {
        const kind = step.wait_for?.kind ?? 'wait';
        lines.push(`    ${id}["⏸ ${esc(step.id)}\\n${esc(kind)}"]:::${cls}`);
        break;
      }

      case 'parallel': {
        const joinStr = step.join ?? 'all';
        lines.push(`    ${id}["⫶ ${esc(step.id)}\\nparallel · ${esc(joinStr)}"]:::${cls}`);
        break;
      }

      default: {
        const agent = step.agent ? agentMap.get(step.agent) : undefined;
        const agentStr = step.agent ? `\\n${esc(step.agent)}` : '';
        const modelStr = agent?.model ? ` · ${esc(shortModel(agent.model))}` : '';
        lines.push(`    ${id}["${esc(step.id)}${agentStr}${modelStr}"]:::${cls}`);
        break;
      }
    }
  }

  // Collect split branch targets to suppress their sequential in-edge
  const branchTargets = new Set<string>();
  for (const step of steps) {
    if (step.type === 'split' && step.branches) {
      for (const branch of step.branches) { branchTargets.add(branch.goto); }
    }
  }

  // Sequential and structural edges
  let prevId: string | null = null;
  for (const step of steps) {
    const id = stepId(step);

    if (prevId !== null && !branchTargets.has(step.id)) {
      const ifLabel = step.if ? `|"if: ${esc(step.if)}"| ` : '';
      lines.push(`    ${prevId} -->${ifLabel}${id}`);
    }

    if (step.type === 'split' && step.branches?.length) {
      for (const branch of step.branches) {
        const targetId = stepIdByName(branch.goto);
        const lbl = buildBranchLabel(branch);
        lines.push(`    ${id} -->|"${esc(lbl)}"| ${targetId}`);
      }
    }

    if (step.type === 'parallel' && step.sub_steps?.length) {
      for (const sub of step.sub_steps) {
        lines.push(`    ${id} --> ${stepId(sub)}`);
      }
    }

    if (step.type === 'foreach' && step.step) {
      lines.push(`    ${id} -.->|"each"| ${stepId(step.step)}`);
    }

    prevId = id;
  }

  // Retry back-edges
  for (const step of steps) {
    if (step.reject_when && step.on_reject?.restart_from) {
      const fromId = stepId(step);
      const toId = stepIdByName(step.on_reject.restart_from);
      const maxStr = step.on_reject.max ? ` max ${step.on_reject.max}x` : '';
      lines.push(`    ${fromId} -.->|"retry${esc(maxStr)}"| ${toId}`);
    }
  }

  lines.push('  end');

  // Click directives (interactive/editor mode only)
  if (interactive) {
    for (const step of steps) {
      const id = stepId(step);
      lines.push(`  click ${id} handleNodeClick "${esc('Edit: ' + step.id)}"`);
    }
  }

  return lines.join('\n');
}

function nodeClass(type: string): string {
  switch (type) {
    case 'split':    return 'splitNode';
    case 'approval': return 'approvalNode';
    case 'foreach':  return 'foreachNode';
    case 'workflow': return 'workflowNode';
    case 'wait_for': return 'waitNode';
    case 'parallel': return 'parallelNode';
    default:         return 'agentNode';
  }
}
