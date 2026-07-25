import { ApiaryConfig, Workflow, Step, EDITABLE_STEP_TYPES } from './parser';

export interface ValidationError {
  severity: 'error' | 'warning';
  message: string;
  /** Diagram node ID for highlighting the relevant node. */
  nodeId?: string;
  workflowId?: string;
  stepId?: string;
  /** Dotted path to the offending field, e.g. "steps.classify.agent". */
  path?: string;
}

// Must match the sid() function in renderer.ts
function sid(s: string): string {
  return 'N_' + s.replace(/[^a-zA-Z0-9]/g, '_');
}

function validateWorkflow(
  wf: Workflow,
  agentIds: Set<string>,
  workflowIds: Set<string>,
  sourceIds: Set<string>,
): ValidationError[] {
  const errors: ValidationError[] = [];
  const stepIds = new Set((wf.steps ?? []).map(s => s.id));

  for (const step of wf.steps ?? []) {
    const nodeId = sid(`${wf.id}_${step.id}`);
    const type = step.type ?? 'agent';

    if (!EDITABLE_STEP_TYPES.has(type)) {
      errors.push({
        severity: 'warning',
        message: `Step "${step.id}" uses unsupported type "${type}" — displayed read-only`,
        nodeId,
        workflowId: wf.id,
        stepId: step.id,
      });
      continue;
    }

    if ((step.depends_on?.length ?? 0) > 0 || (step.seq_depends_on?.length ?? 0) > 0) {
      errors.push({
        severity: 'warning',
        message: `Step "${step.id}" uses v1 DAG syntax (depends_on / seq_depends_on) — displayed read-only`,
        nodeId,
        workflowId: wf.id,
        stepId: step.id,
      });
    }

    if (type === 'agent' || step.type === undefined) {
      if (!step.agent) {
        errors.push({
          severity: 'error',
          message: `Step "${step.id}": agent field is required`,
          nodeId, workflowId: wf.id, stepId: step.id,
          path: `steps.${step.id}.agent`,
        });
      } else if (!agentIds.has(step.agent)) {
        errors.push({
          severity: 'error',
          message: `Step "${step.id}": agent "${step.agent}" is not defined`,
          nodeId, workflowId: wf.id, stepId: step.id,
          path: `steps.${step.id}.agent`,
        });
      }
      if (!step.prompt) {
        errors.push({
          severity: 'error',
          message: `Step "${step.id}": prompt is required for agent steps`,
          nodeId, workflowId: wf.id, stepId: step.id,
          path: `steps.${step.id}.prompt`,
        });
      }
    }

    if (type === 'split') {
      if (!step.branches?.length) {
        errors.push({
          severity: 'error',
          message: `Step "${step.id}": split step requires at least one branch`,
          nodeId, workflowId: wf.id, stepId: step.id,
          path: `steps.${step.id}.branches`,
        });
      }
      for (const branch of step.branches ?? []) {
        if (!stepIds.has(branch.goto)) {
          errors.push({
            severity: 'error',
            message: `Step "${step.id}": branch target "${branch.goto}" does not exist in this workflow`,
            nodeId, workflowId: wf.id, stepId: step.id,
          });
        }
      }
    }

    if (type === 'foreach') {
      if (!step.items) {
        errors.push({
          severity: 'error',
          message: `Step "${step.id}": foreach step requires an items expression`,
          nodeId, workflowId: wf.id, stepId: step.id,
          path: `steps.${step.id}.items`,
        });
      }
    }

    if (type === 'workflow') {
      const refId = step.workflow ?? step.uses;
      if (
        refId &&
        !refId.startsWith('./') &&
        !refId.startsWith('../') &&
        !workflowIds.has(refId)
      ) {
        errors.push({
          severity: 'warning',
          message: `Step "${step.id}": workflow reference "${refId}" is not defined in this file`,
          nodeId, workflowId: wf.id, stepId: step.id,
        });
      }
    }

    if (type === 'approval') {
      if (!step.message && !step.resume_on && !step.abort_on) {
        errors.push({
          severity: 'warning',
          message: `Step "${step.id}": approval step has no message or resume/abort conditions`,
          nodeId, workflowId: wf.id, stepId: step.id,
        });
      }
    }
  }

  const srcRef = wf.trigger?.match?.source;
  if (srcRef && !sourceIds.has(srcRef)) {
    errors.push({
      severity: 'warning',
      message: `Workflow "${wf.id}": trigger source "${srcRef}" is not defined`,
      workflowId: wf.id,
    });
  }

  return errors;
}

export function validateConfig(config: ApiaryConfig): ValidationError[] {
  const agentIds = new Set((config.agents ?? []).map(a => a.id));
  const workflowIds = new Set((config.workflows ?? []).map(w => w.id));
  const sourceIds = new Set((config.sources ?? []).map(s => s.id));
  const errors: ValidationError[] = [];
  for (const wf of config.workflows ?? []) {
    errors.push(...validateWorkflow(wf, agentIds, workflowIds, sourceIds));
  }
  return errors;
}
