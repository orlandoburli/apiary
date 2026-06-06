import { ApiaryConfig, Agent, Source, Workflow, Step, Branch, Trigger } from './parser';

// Produce a valid Mermaid node ID (alphanumeric + underscore)
function sid(s: string): string {
  return 'N_' + s.replace(/[^a-zA-Z0-9]/g, '_');
}

// Escape text for Mermaid node labels (no raw quotes)
function esc(s: string): string {
  return s.replace(/"/g, '#quot;');
}

// Shorten Claude model names for compact display
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
  // Simplify: memory.agent == "po" → agent = po
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

export function renderApiary(config: ApiaryConfig): DiagramResult[] {
  const agentMap = new Map<string, Agent>(
    (config.agents ?? []).map(a => [a.id, a])
  );
  const sourceMap = new Map<string, Source>(
    (config.sources ?? []).map(s => [s.id, s])
  );

  return (config.workflows ?? []).map(wf => ({
    title: wf.id + (wf.description ? ` — ${wf.description}` : ''),
    diagram: renderWorkflow(wf, agentMap, sourceMap),
  }));
}

function renderWorkflow(
  wf: Workflow,
  agentMap: Map<string, Agent>,
  sourceMap: Map<string, Source>,
): string {
  const steps = wf.steps ?? [];

  // Namespace step IDs by workflow to avoid cross-workflow collisions
  const stepId = (step: Step) => sid(`${wf.id}_${step.id}`);
  const stepIdByName = (name: string) => sid(`${wf.id}_${name}`);

  const lines: string[] = [
    'flowchart TD',
    '  classDef agentNode fill:#1a3a6b,stroke:#5b9bd5,color:#dce8f8,stroke-width:1px',
    '  classDef splitNode fill:#3a1a6b,stroke:#9b5bd5,color:#e8dcf8,stroke-width:1px',
    '  classDef approvalNode fill:#5a4a0a,stroke:#c8a83a,color:#f8f0dc,stroke-width:1px',
    '  classDef sourceNode fill:#0a3a1a,stroke:#3aaa6a,color:#dcf8e8,stroke-width:1px',
  ];

  // Source node (outside the subgraph)
  const sourceKey = wf.trigger?.match?.source;
  const source = sourceKey ? sourceMap.get(sourceKey) : undefined;
  const srcId = srcNodeId(sourceKey);

  if (srcId && source) {
    lines.push(`  ${srcId}["📦 ${sourceDisplay(source)}"]:::sourceNode`);
  } else if (srcId) {
    lines.push(`  ${srcId}["📦 ${esc(sourceKey!)}"]:::sourceNode`);
  }

  // Source → first step edge (crosses into the subgraph — valid in Mermaid)
  if (srcId && steps.length > 0) {
    const firstId = stepId(steps[0]);
    const lbl = buildTriggerLabel(wf.trigger);
    const edge = lbl ? `-->|"${esc(lbl)}"| ` : '--> ';
    lines.push(`  ${srcId} ${edge}${firstId}`);
  }

  // Open subgraph
  const wfLabel = esc(wf.id + (wf.description ? ` — ${wf.description}` : ''));
  lines.push(`  subgraph ${sid('WF_' + wf.id)}["${wfLabel}"]`);

  // Step node declarations
  for (const step of steps) {
    const id = stepId(step);
    if (step.type === 'split') {
      lines.push(`    ${id}{"${esc(step.id)}\\nsplit"}:::splitNode`);
    } else if (step.type === 'approval') {
      lines.push(`    ${id}[/"⏳ ${esc(step.id)}\\napproval"/]:::approvalNode`);
    } else {
      const agent = step.agent ? agentMap.get(step.agent) : undefined;
      const agentStr = step.agent ? `\\n${esc(step.agent)}` : '';
      const modelStr = agent?.model ? ` · ${esc(shortModel(agent.model))}` : '';
      lines.push(`    ${id}["${esc(step.id)}${agentStr}${modelStr}"]:::agentNode`);
    }
  }

  // Collect branch target step IDs so we skip the sequential edge into them —
  // they receive their only incoming edge from the split node itself.
  const branchTargets = new Set<string>();
  for (const step of steps) {
    if (step.type === 'split' && step.branches) {
      for (const branch of step.branches) { branchTargets.add(branch.goto); }
    }
  }

  // Step edges (sequential flow + conditional labels)
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

    prevId = id;
  }

  // Retry loops (dashed back-edges)
  for (const step of steps) {
    if (step.reject_when && step.on_reject?.restart_from) {
      const fromId = stepId(step);
      const toId = stepIdByName(step.on_reject.restart_from);
      const maxStr = step.on_reject.max ? ` max ${step.on_reject.max}x` : '';
      lines.push(`    ${fromId} -.->|"retry${esc(maxStr)}"| ${toId}`);
    }
  }

  lines.push('  end');

  return lines.join('\n');
}

function srcNodeId(sourceKey?: string): string | null {
  return sourceKey ? sid('SRC_' + sourceKey) : null;
}
