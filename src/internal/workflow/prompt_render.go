package workflow

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// promptAccessorRe matches a plain accessor placeholder such as
// `${{ input.dry_run }}` or `${{ steps.tests.output }}`. Only a dot path is
// matched: anything containing an operator (`${{ memory.verdict == "x" }}`) or
// a bare alias (`${{ item }}`) is left alone — those are not text substitutions.
var promptAccessorRe = regexp.MustCompile(`\$\{\{\s*([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z0-9_-]+)+)\s*\}\}`)

// renderPromptTemplate substitutes plain `${{ <root>.<path> }}` accessors in a
// step prompt with the same data an `if:` condition sees (input, event, memory,
// cell, steps, task). It is a literal text replacement, not expression
// evaluation: only dot-path accessors are touched.
//
// A placeholder whose root or path cannot be resolved is left UNCHANGED. An
// unresolved placeholder is visible to a human reading the prompt; blanking it
// would reproduce the silent-wrong-text bug this rendering exists to fix.
func renderPromptTemplate(prompt string, ctx EvalContext) string {
	if prompt == "" || !strings.Contains(prompt, "${{") {
		return prompt
	}
	return promptAccessorRe.ReplaceAllStringFunc(prompt, func(match string) string {
		sub := promptAccessorRe.FindStringSubmatch(match)
		path := append([]string{sub[1]}, strings.Split(sub[2][1:], ".")...)
		v, err := resolveAccessor(path, ctx)
		if err != nil {
			return match
		}
		switch v.kind {
		case kString:
			return v.s
		case kNumber:
			return formatPromptNumber(v.n)
		case kList:
			return strings.Join(v.list, ", ")
		default: // kMissing — keep the placeholder visible
			return match
		}
	})
}

func formatPromptNumber(n float64) string {
	if n == float64(int64(n)) {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'g', -1, 64)
}

// renderStepTree renders prompt templates in step and its in-dispatch agent
// children (parallel children, a foreach inner step). It is called at dispatch
// time on the scheduler goroutine so r.stepStates/r.event are never read from a
// worker. Nested slices/pointers are copied: the live workflow config must not
// be mutated. A `type: workflow` node is left untouched — its child instance
// re-enters driveDAG, which renders its own steps at their dispatch.
func renderStepTree(step config.StepConfig, ctx EvalContext) config.StepConfig {
	step.Prompt = renderPromptTemplate(step.Prompt, ctx)
	step.SummaryPrompt = renderPromptTemplate(step.SummaryPrompt, ctx)
	if step.StepType() == config.StepTypeWorkflow {
		return step
	}
	if len(step.SubSteps) > 0 {
		children := make([]config.StepConfig, len(step.SubSteps))
		for i, child := range step.SubSteps {
			children[i] = renderStepTree(child, ctx)
		}
		step.SubSteps = children
	}
	if step.Step != nil {
		inner := renderStepTree(*step.Step, ctx)
		step.Step = &inner
	}
	return step
}

// inputValues renders a task's structured input map to strings for prompt
// templates and the APIARY_INPUT_* environment overlay. Values are formatted
// the same way memory values are (scalars plain, arrays/objects compact JSON).
func inputValues(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = renderValue(v)
	}
	return out
}
