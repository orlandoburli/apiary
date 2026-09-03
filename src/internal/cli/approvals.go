package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/orlandoburli/apiary/internal/db"
)

// The approvals CLI: list what is waiting for a human, and answer it.
//
// An approval step parks its workflow until someone responds. Until now the only
// way to respond was the dashboard's y/n — which could not send the step's
// declared fields — or a signed webhook, which is a lot of ceremony for an action
// taken on the same machine. These commands close that gap and are scriptable:
// every field can be supplied with --field, and the exit code says what happened.
//
// Exit codes:
//
//	0  the response resolved the gate; the workflow is resuming
//	3  the response was recorded but the gate still waits (a quorum gate)
//	4  the request is unknown, or was already answered
//	1  anything else (transport, validation)

func newApprovalsCmd() *cobra.Command {
	var (
		status string
		limit  int
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "approvals [request-id]",
		Short: "List approval requests waiting for an answer, or show one in detail",
		Long: "List approval requests waiting for an answer, or show one in detail.\n\n" +
			"An approval step parks its workflow until it is answered. Answer one with\n" +
			"`apiary approve <request-id>` or `apiary reject <request-id>`; a request that\n" +
			"declares fields prompts for them when stdin is a terminal.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				return showApproval(args[0], asJSON)
			}
			return listApprovals(status, limit, asJSON)
		},
	}
	cmd.Flags().StringVar(&status, "status", "pending", "filter by status (pending, approved, rejected, timed_out, or all)")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum requests to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newApproveCmd() *cobra.Command {
	var (
		fields  []string
		comment string
		yes     bool
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "approve <request-id>",
		Short: "Approve a waiting approval request",
		Long: "Approve a waiting approval request.\n\n" +
			"When the step declares fields, supply them with --field name=value (repeatable).\n" +
			"On a terminal, any field left unsupplied is prompted for; off a terminal a\n" +
			"missing required field is an error, never a prompt — so a script never hangs.\n\n" +
			"Submitted values reach the workflow as ${{ memory.<field> }}, which is how a\n" +
			"choice field steers what runs next.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return respondToApproval(args[0], "approve", fields, comment, yes, asJSON)
		},
	}
	cmd.Flags().StringArrayVar(&fields, "field", nil, "field value as name=value (repeatable)")
	cmd.Flags().StringVar(&comment, "comment", "", "feedback recorded with the response")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newRejectCmd() *cobra.Command {
	var (
		comment string
		yes     bool
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "reject <request-id>",
		Short: "Reject a waiting approval request",
		Long: "Reject a waiting approval request.\n\n" +
			"A rejection ends the gate immediately and fails the workflow, so it never\n" +
			"collects the step's fields — only --comment, which reaches the workflow as\n" +
			"${{ memory.approval_feedback }}.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return respondToApproval(args[0], "reject", nil, comment, yes, asJSON)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "reason recorded with the rejection")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

// listApprovals renders the pending queue. "all" is spelled as an empty status
// filter on the wire, matching ListApprovalRequests.
func listApprovals(status string, limit int, asJSON bool) error {
	if strings.EqualFold(status, "all") {
		status = ""
	}
	query := url.Values{}
	if status != "" {
		query.Set("status", status)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var requests []db.ApprovalRequest
	if err := ipcGetJSON("/approvals?"+query.Encode(), &requests); err != nil {
		if isDaemonDown(err) {
			return daemonDownHint()
		}
		return err
	}

	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(requests)
	}
	if len(requests) == 0 {
		fmt.Println(instMuted.Render("No approval requests are waiting."))
		return nil
	}

	// The request id is never truncated: it is the argument the operator copies
	// into `apiary approve`, and an elided one cannot be typed back. The column
	// is sized to the widest id present so the rest of the table still lines up.
	idWidth := len("REQUEST")
	forWidth := len("FOR")
	for _, r := range requests {
		idWidth = max(idWidth, len(r.ID))
		forWidth = max(forWidth, len(approvalContextLabel(r)))
	}
	forWidth = min(forWidth, 28)

	fmt.Println()
	fmt.Printf("  %s\n", instHeader.Render(fmt.Sprintf("%-*s %-*s %-16s %-14s %-9s %s",
		idWidth, "REQUEST", forWidth, "FOR", "WORKFLOW", "STEP", "PARKED", "EXPIRES")))
	for _, r := range requests {
		fmt.Printf("  %-*s %-*s %-16s %-14s %-9s %s\n",
			idWidth, r.ID,
			forWidth, truncate(approvalContextLabel(r), forWidth),
			truncate(r.WorkflowID, 16),
			truncate(r.StepID, 14),
			shortDuration(time.Since(r.CreatedAt)),
			expiresIn(r.ExpiresAt))
	}
	fmt.Println()
	fmt.Println(instMuted.Render("  Answer one with: ") +
		instHeader.Render("apiary approve <request-id>"))
	fmt.Println()
	return nil
}

// showApproval prints one request with the fields it expects, so an operator can
// see what --field flags an unattended answer needs.
func showApproval(id string, asJSON bool) error {
	request, err := fetchApproval(id)
	if err != nil {
		return err
	}
	if request == nil {
		fmt.Println(instErr.Render("Not found: ") + id)
		os.Exit(4)
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(request)
	}

	fmt.Println()
	fmt.Println("  " + instHeader.Render(request.ID))
	if request.Message != "" {
		fmt.Println("  " + request.Message)
	}
	fmt.Println()
	printApprovalContext(request)
	fmt.Printf("  %-12s %s\n", instMuted.Render("workflow"), request.WorkflowID)
	fmt.Printf("  %-12s %s\n", instMuted.Render("step"), request.StepID)
	fmt.Printf("  %-12s %s\n", instMuted.Render("status"), request.Status)
	fmt.Printf("  %-12s %s ago\n", instMuted.Render("parked"), shortDuration(time.Since(request.CreatedAt)))
	if request.ExpiresAt != nil {
		fmt.Printf("  %-12s %s\n", instMuted.Render("expires"), expiresIn(request.ExpiresAt))
	}
	if len(request.Approvers) > 0 {
		required := max(request.RequiredApprovals, 1)
		fmt.Printf("  %-12s %s (%d required)\n", instMuted.Render("approvers"),
			strings.Join(request.Approvers, ", "), required)
	}

	if len(request.Fields) > 0 {
		fmt.Println()
		fmt.Println("  " + instHeader.Render("Fields"))
		for _, f := range approvalFieldsOf(request) {
			spec := f.Type
			if len(f.Options) > 0 {
				spec += "  [" + strings.Join(f.Options, " | ") + "]"
			}
			if f.Required {
				spec += "  " + instWarn.Render("required")
			}
			fmt.Printf("    %-16s %s\n", f.Name, instMuted.Render(spec))
		}
	}
	fmt.Println()
	return nil
}

// respondToApproval collects the response and posts it.
func respondToApproval(id, decision string, fieldFlags []string, comment string, yes, asJSON bool) error {
	request, err := fetchApproval(id)
	if err != nil {
		return err
	}
	if request == nil {
		fmt.Println(instErr.Render("Not found: ") + id)
		os.Exit(4)
	}
	if request.Status != db.ApprovalPending && request.Status != db.ApprovalEscalated {
		fmt.Println(instErr.Render("Already answered: ") + id +
			instMuted.Render(" (status "+request.Status+")"))
		os.Exit(4)
	}

	// Show what this request is actually for before asking anything else —
	// including before any field prompt below — so an operator never answers
	// blind. This used to run after field collection and right before the
	// (default-no) confirmation, which is exactly backwards (#473).
	if !asJSON {
		fmt.Println()
		fmt.Println("  " + instHeader.Render(request.ID))
		printApprovalContext(request)
		fmt.Printf("  %-12s %s\n", instMuted.Render("step"), request.StepID)
		if request.Message != "" {
			fmt.Println()
			fmt.Println("  " + request.Message)
		}
	}

	supplied, err := parseFieldFlags(fieldFlags)
	if err != nil {
		return err
	}

	// A rejection ends the gate, so its fields are never collected.
	values := map[string]any{}
	if decision == "approve" {
		values, err = resolveApprovalValues(request, supplied)
		if err != nil {
			return err
		}
	}

	if !yes && !asJSON && isInteractive() {
		if len(values) > 0 {
			fmt.Println()
			for _, name := range sortedKeys(values) {
				fmt.Printf("  %-16s %v\n", instMuted.Render(name), values[name])
			}
		}
		fmt.Println()
		verb := strings.ToUpper(decision[:1]) + decision[1:]
		if decision == "approve" {
			// Approving is the common case an operator reaches for this command
			// to do, so a bare Enter takes it — the context printed above is
			// what makes that safe to assume.
			if !confirmDefault(fmt.Sprintf("  %s %s? [Y/n] ", verb, id), true) {
				fmt.Println(instMuted.Render("  Cancelled."))
				return nil
			}
		} else {
			// Rejection ends the gate and fails the workflow immediately, so it
			// keeps requiring explicit, unambiguous input — a bare Enter here
			// must never be read as "reject".
			if !confirm(fmt.Sprintf("  %s %s? [y/N] ", verb, id)) {
				fmt.Println(instMuted.Render("  Cancelled."))
				return nil
			}
		}
	}

	return postApprovalResponse(request, decision, values, comment, asJSON)
}

// printApprovalContext prints the ticket/PR this request resolves to, when
// known — TicketRef/TicketURL/PRNumber/PRURL are resolved by the daemon via the
// request's TaskID (Dispatcher.enrichApprovalContext in internal/daemon) and
// travel on db.ApprovalRequest itself. Shared by `apiary approvals <id>` and
// the pre-decision context in `apiary approve`/`apiary reject`, so there is one
// place that knows how to render it (#473).
func printApprovalContext(request *db.ApprovalRequest) {
	if request.TicketRef != "" {
		line := request.TicketRef
		if request.TicketURL != "" {
			line += "  " + instMuted.Render(request.TicketURL)
		}
		fmt.Printf("  %-12s %s\n", instMuted.Render("ticket"), line)
	}
	if request.PRNumber != 0 {
		line := fmt.Sprintf("#%d", request.PRNumber)
		if request.PRURL != "" {
			line += "  " + instMuted.Render(request.PRURL)
		}
		fmt.Printf("  %-12s %s\n", instMuted.Render("pr"), line)
	}
}

// approvalContextLabel condenses TicketRef/PRNumber into the `apiary approvals`
// table's FOR column: "source/ref" and/or "PR #n", joined; "—" when the
// request's task resolved to neither (or has no task at all).
func approvalContextLabel(r db.ApprovalRequest) string {
	var parts []string
	if r.TicketRef != "" {
		parts = append(parts, r.TicketRef)
	}
	if r.PRNumber != 0 {
		parts = append(parts, fmt.Sprintf("PR #%d", r.PRNumber))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

// postApprovalResponse sends the answer and maps the outcome onto an exit code.
//
// The actor is recorded for the audit timeline; the daemon only authorizes it
// when the request names approvers, which an operator gate does not.
func postApprovalResponse(request *db.ApprovalRequest, decision string, values map[string]any, comment string, asJSON bool) error {
	actor := approvalActor()
	payload := db.ApprovalResponse{
		Decision: decision,
		Actor:    actor,
		Channel:  "cli",
		Feedback: comment,
		Values:   values,
		// Stable across retries: re-running the same command after a dropped
		// connection records the answer once, never twice.
		IdempotencyKey: fmt.Sprintf("cli:%s:%s:%s", request.ID, actor, decision),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var result struct {
		Recorded  bool                `json:"recorded"`
		Resolved  bool                `json:"resolved"`
		Approvals int                 `json:"approvals"`
		Required  int                 `json:"required"`
		Request   *db.ApprovalRequest `json:"request"`
	}
	status, err := ipcDoBody(http.MethodPost,
		"/approvals/"+url.PathEscape(request.ID)+"/respond", body, &result)
	switch {
	case status == 0 && err != nil:
		return daemonDownHint()
	case status == http.StatusNotFound:
		fmt.Println(instErr.Render("Not found: ") + request.ID)
		os.Exit(4)
	case status == http.StatusConflict:
		fmt.Println(instErr.Render("Already answered: ") + request.ID)
		os.Exit(4)
	case err != nil:
		fmt.Println(instErr.Render("Error: ") + err.Error())
		os.Exit(1)
	}

	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	verb := map[string]string{"approve": "Approved", "reject": "Rejected"}[decision]
	if !result.Resolved {
		// Only a quorum gate gets here: the answer is durable, but the workflow
		// stays parked until the remaining approvers respond.
		fmt.Printf("%s %s %s\n", instWarn.Render("○"), verb,
			instMuted.Render(fmt.Sprintf("— %d of %d approvals recorded, still waiting",
				result.Approvals, result.Required)))
		os.Exit(3)
	}
	outcome := "workflow resuming"
	if decision == "reject" {
		outcome = "workflow aborting"
	}
	fmt.Printf("%s %s %s\n", instOK.Render("✓"), verb, instMuted.Render("— "+outcome))
	return nil
}

// resolveApprovalValues merges --field flags with prompts, then type-checks the
// result the same way the daemon does.
//
// Off a terminal a missing required field fails loudly instead of prompting: a
// scripted approval that blocks on stdin is worse than one that errors.
func resolveApprovalValues(request *db.ApprovalRequest, supplied map[string]string) (map[string]any, error) {
	fields := approvalFieldsOf(request)
	if len(fields) == 0 {
		return nil, nil
	}

	// Reject unknown --field names rather than silently dropping them; a typo
	// would otherwise look like a successful answer with a missing value.
	known := map[string]bool{}
	for _, f := range fields {
		known[f.Name] = true
	}
	for name := range supplied {
		if !known[name] {
			return nil, fmt.Errorf("unknown field %q (see `apiary approvals %s`)", name, request.ID)
		}
	}

	interactive := isInteractive()
	values := map[string]any{}
	reader := bufio.NewReader(os.Stdin)

	for _, f := range fields {
		raw, ok := supplied[f.Name]
		if !ok {
			if !interactive {
				if f.Required {
					return nil, fmt.Errorf("field %q is required: pass --field %s=<value>", f.Name, f.Name)
				}
				continue
			}
			raw = promptApprovalField(reader, f)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			if f.Required {
				return nil, fmt.Errorf("field %q is required", f.Name)
			}
			continue
		}
		value, err := coerceApprovalValue(f, raw)
		if err != nil {
			return nil, err
		}
		values[f.Name] = value
	}
	return values, nil
}

// promptApprovalField asks for one field on a terminal. Choices are numbered so
// the answer is a keystroke rather than a spelling test.
func promptApprovalField(reader *bufio.Reader, f approvalFieldSpec) string {
	label := f.Label
	if label == "" {
		label = f.Name
	}

	if len(f.Options) > 0 {
		fmt.Println()
		fmt.Println("  " + instHeader.Render(label))
		for i, opt := range f.Options {
			fmt.Printf("    %s) %s\n", instOK.Render(strconv.Itoa(i+1)), opt)
		}
		fmt.Print("  choose [1]: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return f.Options[0]
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(f.Options) {
			return f.Options[n-1]
		}
		return line // let coerceApprovalValue reject it by name
	}

	hint := f.Type
	if f.Required {
		hint += ", required"
	}
	fmt.Printf("\n  %s %s: ", instHeader.Render(label), instMuted.Render("("+hint+")"))
	line, _ := reader.ReadString('\n')
	return line
}

// coerceApprovalValue converts a typed-in string to the field's declared type,
// mirroring the daemon's validateApprovalResponse so a mistake is caught here
// rather than after a round trip.
func coerceApprovalValue(f approvalFieldSpec, raw string) (any, error) {
	switch f.Type {
	case "boolean":
		switch strings.ToLower(raw) {
		case "true", "yes", "y", "1":
			return true, nil
		case "false", "no", "n", "0":
			return false, nil
		}
		return nil, fmt.Errorf("field %q must be true or false", f.Name)

	case "number":
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("field %q must be a number", f.Name)
		}
		return n, nil

	case "choice":
		for _, opt := range f.Options {
			if opt == raw {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("field %q must be one of: %s", f.Name, strings.Join(f.Options, ", "))

	default:
		return raw, nil
	}
}

// approvalFieldSpec is one declared field, normalized out of the request's JSON.
type approvalFieldSpec struct {
	Name     string
	Label    string
	Type     string
	Required bool
	Options  []string
}

func approvalFieldsOf(request *db.ApprovalRequest) []approvalFieldSpec {
	out := make([]approvalFieldSpec, 0, len(request.Fields))
	for _, raw := range request.Fields {
		name, _ := raw["name"].(string)
		if name == "" {
			continue
		}
		label, _ := raw["label"].(string)
		typeName, _ := raw["type"].(string)
		if typeName == "" {
			typeName = "string"
		}
		required, _ := raw["required"].(bool)
		out = append(out, approvalFieldSpec{
			Name:     name,
			Label:    label,
			Type:     typeName,
			Required: required,
			Options:  jsonStrings(raw["options"]),
		})
	}
	return out
}

// jsonStrings reads a []string that has been through JSON (and so arrives as
// []any), tolerating the in-memory form too.
func jsonStrings(v any) []string {
	switch vals := v.(type) {
	case []string:
		return vals
	case []any:
		out := make([]string, 0, len(vals))
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// parseFieldFlags turns --field name=value pairs into a map. The value may
// contain '=' — only the first one separates.
func parseFieldFlags(flags []string) (map[string]string, error) {
	out := make(map[string]string, len(flags))
	for _, raw := range flags {
		name, value, found := strings.Cut(raw, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			return nil, fmt.Errorf("invalid --field %q: expected name=value", raw)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("field %q given twice", name)
		}
		out[name] = value
	}
	return out, nil
}

// fetchApproval loads one request by id. The daemon has no per-request GET, so it
// filters the list — the pending queue is small by nature.
func fetchApproval(id string) (*db.ApprovalRequest, error) {
	var requests []db.ApprovalRequest
	if err := ipcGetJSON("/approvals?limit=500", &requests); err != nil {
		if isDaemonDown(err) {
			return nil, daemonDownHint()
		}
		return nil, err
	}
	for i := range requests {
		if requests[i].ID == id {
			return &requests[i], nil
		}
	}
	return nil, nil
}

// approvalActor names who answered, for the execution timeline. It is provenance,
// not authentication: apiary runs locally, and the daemon only checks the actor
// against an approver list when the step declares one.
func approvalActor() string {
	if actor := os.Getenv("USER"); actor != "" {
		return actor
	}
	return "cli-user"
}

// isInteractive reports whether stdin is a terminal, deciding whether a missing
// field is prompted for or is an error.
//
// It asks term.IsTerminal rather than testing for a character device: /dev/null
// is a character device too, so the cheaper check calls a redirected stdin
// interactive and a scripted approval would sit waiting for input that never
// comes.
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func isDaemonDown(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "connect: no such file or directory") ||
		strings.Contains(err.Error(), "connection refused"))
}

func expiresIn(at *time.Time) string {
	if at == nil {
		return instMuted.Render("never")
	}
	remaining := time.Until(*at)
	if remaining <= 0 {
		return instWarn.Render("expired")
	}
	return "in " + shortDuration(remaining)
}

// shortDuration renders an age or a countdown compactly enough for a table
// column: 45s, 12m, 3h, 2d.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ipcDoBody is ipcDo with a request body.
func ipcDoBody(method, path string, body []byte, out any) (int, error) {
	return ipcRequest(method, path, bytes.NewReader(body), out)
}
