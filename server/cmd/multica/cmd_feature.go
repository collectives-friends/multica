package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var featureCmd = newFeatureCommand()

// Definitions come from the server registry, including plugin-owned flags.
// Keep metadata intact so newer definitions remain inspectable by this CLI.
type featureSnapshot struct {
	Definitions []map[string]any             `json:"definitions"`
	Resolved    map[string]featureResolution `json:"resolved"`
	Overrides   struct {
		Workspace map[string]json.RawMessage `json:"workspace"`
		Member    map[string]json.RawMessage `json:"member"`
	} `json:"overrides"`
}

type featureResolution struct {
	Value  *bool  `json:"value"`
	Source string `json:"source"`
	Locked bool   `json:"locked"`
}

func newFeatureCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "feature", Short: "Inspect and override workspace feature flags"}
	list := &cobra.Command{Use: "list", Short: "List feature flags and their effective values", Args: exactArgs(0), RunE: runFeatureList}
	list.Flags().String("group", "", "Filter by feature group")
	list.Flags().String("state", "", "Filter by effective state: on or off")
	list.Flags().String("plugin", "", "Filter by plugin key")
	list.Flags().String("output", "table", "Output format: table or json")
	get := &cobra.Command{Use: "get <key>", Short: "Show a feature definition, effective value, and overrides", Args: exactArgs(1), RunE: runFeatureGet}
	get.Flags().String("output", "json", "Output format: table or json")
	set := &cobra.Command{Use: "set <key> on|off", Short: "Set a workspace override (or your personal override)", Args: exactArgs(2), RunE: runFeatureSet}
	set.Flags().Bool("personal", false, "Set your personal override instead of the workspace override")
	set.Flags().Bool("locked", false, "Lock the workspace value for all members")
	set.Flags().String("reason", "", "Reason for the workspace override")
	set.Flags().String("output", "json", "Output format: table or json")
	unset := &cobra.Command{Use: "unset <key>", Short: "Remove a workspace override (or your personal override)", Args: exactArgs(1), RunE: runFeatureUnset}
	unset.Flags().Bool("personal", false, "Remove your personal override instead of the workspace override")
	unset.Flags().String("output", "json", "Output format: table or json")
	history := &cobra.Command{Use: "history <key>", Short: "Show the audit history of a feature flag", Args: exactArgs(1), RunE: runFeatureHistory}
	history.Flags().Int("limit", 50, "Maximum number of audit entries")
	history.Flags().String("before", "", "Pagination cursor from next_before")
	history.Flags().String("output", "table", "Output format: table or json")
	cmd.AddCommand(list, get, set, unset, history)
	return cmd
}

func featureClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && output != "table" {
		return nil, "", fmt.Errorf("--output must be table or json")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	if client.WorkspaceID == "" {
		return nil, "", fmt.Errorf("workspace ID is required: use --workspace-id, MULTICA_WORKSPACE_ID, or your profile")
	}
	return client, "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/feature-flags", nil
}

func loadFeatureSnapshot(ctx context.Context, client *cli.APIClient, path string) (featureSnapshot, error) {
	var result featureSnapshot
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return result, fmt.Errorf("get feature flags: %w", err)
	}
	if result.Definitions == nil || result.Resolved == nil {
		return result, fmt.Errorf("invalid feature flags response: missing definitions or resolved values")
	}
	for _, definition := range result.Definitions {
		key := strVal(definition, "key")
		resolution, ok := result.Resolved[key]
		if key == "" || !ok || resolution.Value == nil || resolution.Source == "" {
			return result, fmt.Errorf("invalid feature flags response: missing resolution for %q", key)
		}
	}
	return result, nil
}

func runFeatureList(cmd *cobra.Command, _ []string) error {
	state, _ := cmd.Flags().GetString("state")
	if state != "" && state != "on" && state != "off" {
		return fmt.Errorf("--state must be on or off")
	}
	client, path, err := featureClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	snapshot, err := loadFeatureSnapshot(ctx, client, path)
	if err != nil {
		return err
	}
	group, _ := cmd.Flags().GetString("group")
	plugin, _ := cmd.Flags().GetString("plugin")
	flags := make([]map[string]any, 0, len(snapshot.Definitions))
	rows := make([][]string, 0, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		key := strVal(definition, "key")
		resolved := snapshot.Resolved[key]
		if group != "" && strVal(definition, "group") != group {
			continue
		}
		if state != "" && *resolved.Value != (state == "on") {
			continue
		}
		if plugin != "" && !strings.HasPrefix(key, "plugin:"+plugin+":") {
			continue
		}
		flag := make(map[string]any, len(definition)+3)
		for k, v := range definition {
			flag[k] = v
		}
		flag["value"], flag["source"], flag["locked"] = *resolved.Value, resolved.Source, resolved.Locked
		flags = append(flags, flag)
		rows = append(rows, []string{key, strVal(definition, "label"), strVal(definition, "group"), strVal(definition, "plugin_name"), featureState(*resolved.Value), resolved.Source, strconv.FormatBool(resolved.Locked)})
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, flags)
	}
	cli.PrintTable(os.Stdout, []string{"KEY", "LABEL", "GROUP", "PLUGIN", "STATE", "SOURCE", "LOCKED"}, rows)
	return nil
}

func runFeatureGet(cmd *cobra.Command, args []string) error {
	client, path, err := featureClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	snapshot, err := loadFeatureSnapshot(ctx, client, path)
	if err != nil {
		return err
	}
	for _, definition := range snapshot.Definitions {
		if strVal(definition, "key") != args[0] {
			continue
		}
		resolved := snapshot.Resolved[args[0]]
		overrides := map[string]json.RawMessage{}
		if v, ok := snapshot.Overrides.Workspace[args[0]]; ok {
			overrides["workspace"] = v
		}
		if v, ok := snapshot.Overrides.Member[args[0]]; ok {
			overrides["member"] = v
		}
		if output, _ := cmd.Flags().GetString("output"); output == "json" {
			return cli.PrintJSON(os.Stdout, map[string]any{"definition": definition, "value": *resolved.Value, "source": resolved.Source, "locked": resolved.Locked, "overrides": overrides})
		}
		definitionJSON, _ := json.Marshal(definition)
		overrideJSON, _ := json.Marshal(overrides)
		cli.PrintTable(os.Stdout, []string{"FIELD", "VALUE"}, [][]string{{"Key", args[0]}, {"Label", strVal(definition, "label")}, {"State", featureState(*resolved.Value)}, {"Source", resolved.Source}, {"Locked", strconv.FormatBool(resolved.Locked)}, {"Definition", string(definitionJSON)}, {"Overrides", string(overrideJSON)}})
		return nil
	}
	return fmt.Errorf("unknown feature flag %q", args[0])
}

func runFeatureSet(cmd *cobra.Command, args []string) error {
	if args[1] != "on" && args[1] != "off" {
		return fmt.Errorf("feature state must be on or off")
	}
	personal, _ := cmd.Flags().GetBool("personal")
	if personal && cmd.Flags().Changed("locked") {
		return fmt.Errorf("--locked cannot be used with --personal")
	}
	if personal && cmd.Flags().Changed("reason") {
		return fmt.Errorf("--reason applies only to workspace overrides")
	}
	client, path, err := featureClient(cmd)
	if err != nil {
		return err
	}
	path += "/" + url.PathEscape(args[0])
	body := map[string]any{"enabled": args[1] == "on"}
	scope := "member"
	if !personal {
		scope = "workspace"
		path += "/workspace"
		body["locked"], _ = cmd.Flags().GetBool("locked")
		if cmd.Flags().Changed("reason") {
			body["reason"], _ = cmd.Flags().GetString("reason")
		}
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if err := client.PutJSON(ctx, path, body, nil); err != nil {
		return fmt.Errorf("set feature override: %w", err)
	}
	result := map[string]any{"key": args[0], "scope": scope, "enabled": body["enabled"]}
	if !personal {
		result["locked"] = body["locked"]
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stdout, "Feature %s %s override set to %s.\n", args[0], scope, args[1])
	return nil
}

func runFeatureUnset(cmd *cobra.Command, args []string) error {
	client, path, err := featureClient(cmd)
	if err != nil {
		return err
	}
	personal, _ := cmd.Flags().GetBool("personal")
	path += "/" + url.PathEscape(args[0])
	scope := "member"
	if !personal {
		scope = "workspace"
		path += "/workspace"
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if err := client.DeleteJSON(ctx, path); err != nil {
		return fmt.Errorf("unset feature override: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"key": args[0], "scope": scope, "deleted": true})
	}
	fmt.Fprintf(os.Stdout, "Feature %s %s override removed.\n", args[0], scope)
	return nil
}

func runFeatureHistory(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 {
		return fmt.Errorf("--limit must be positive")
	}
	client, path, err := featureClient(cmd)
	if err != nil {
		return err
	}
	params := url.Values{"key": {args[0]}, "limit": {strconv.Itoa(limit)}}
	if before, _ := cmd.Flags().GetString("before"); before != "" {
		params.Set("before", before)
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var result struct {
		Entries    []map[string]any `json:"entries"`
		NextBefore string           `json:"next_before,omitempty"`
	}
	if err := client.GetJSON(ctx, path+"/audit?"+params.Encode(), &result); err != nil {
		return fmt.Errorf("get feature history: %w", err)
	}
	if result.Entries == nil {
		return fmt.Errorf("invalid feature history response: missing entries")
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	rows := make([][]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		rows = append(rows, []string{strVal(entry, "created_at"), strVal(entry, "scope_type"), strVal(entry, "action"), featureAuditState(entry["old_enabled"]), featureAuditState(entry["new_enabled"]), featureAuditState(entry["old_locked"]), featureAuditState(entry["new_locked"]), strVal(entry, "actor_type") + ":" + strVal(entry, "actor_id"), strVal(entry, "reason")})
	}
	cli.PrintTable(os.Stdout, []string{"CREATED", "SCOPE", "ACTION", "OLD", "NEW", "OLD LOCK", "NEW LOCK", "ACTOR", "REASON"}, rows)
	if result.NextBefore != "" {
		fmt.Fprintf(os.Stdout, "Next page: --before %s\n", result.NextBefore)
	}
	return nil
}

func featureState(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func featureAuditState(value any) string {
	if enabled, ok := value.(bool); ok {
		return featureState(enabled)
	}
	return "-"
}
