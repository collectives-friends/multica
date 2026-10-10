package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const featureFixture = `{"definitions":[
 {"key":"board","label":"Board view","description":"Manage a board","group":"views","default":true,"scope_policy":"member","public":true},
 {"key":"plugin:calendar:week","label":"Weekly calendar","description":"Plugin week","group":"views","default":false,"scope_policy":"workspace","parent":"board","public":true,"plugin_name":"Calendar","plugin_installation_id":"installation-1"},
 {"key":"plugin:calendar2:week","label":"Other calendar","group":"views","default":false,"scope_policy":"workspace","public":true},
 {"key":"metrics","label":"Metrics","group":"reports","default":false,"scope_policy":"workspace","public":true}],
 "resolved":{"board":{"value":true,"source":"member","locked":false},"plugin:calendar:week":{"value":false,"source":"workspace_locked","locked":true},"plugin:calendar2:week":{"value":false,"source":"default","locked":false},"metrics":{"value":true,"source":"deployment","locked":true}},
 "overrides":{"workspace":{"plugin:calendar:week":{"enabled":false,"locked":true}},"member":{"board":true}}}`

func featureTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT", "MULTICA_PROFILE"} {
		t.Setenv(name, "")
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	setCLITestServerEnv(t, srv.URL)
}

func executeFeature(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("workspace-id", "", "")
	root.PersistentFlags().String("server-url", "", "")
	root.PersistentFlags().String("profile", "", "")
	root.AddCommand(newFeatureCommand())
	root.SetArgs(append([]string{"feature"}, args...))
	return captureStdout(t, root.Execute)
}

func TestFeatureListFiltersAndUsesAuthenticatedWorkspace(t *testing.T) {
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/workspaces/ws-1/feature-flags" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Workspace-ID") != "ws-1" {
			t.Error("missing auth/workspace headers")
		}
		_, _ = w.Write([]byte(featureFixture))
	})
	for _, tc := range []struct {
		name    string
		filters []string
		want    []string
	}{
		{"all", nil, []string{"board", "plugin:calendar:week", "plugin:calendar2:week", "metrics"}},
		{"group", []string{"--group", "reports"}, []string{"metrics"}},
		{"state", []string{"--state", "on"}, []string{"board", "metrics"}},
		{"combined", []string{"--group", "views", "--state", "off", "--plugin", "calendar"}, []string{"plugin:calendar:week"}},
		{"empty", []string{"--group", "missing"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"list", "--output", "json"}, tc.filters...)
			out, err := executeFeature(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatal(err)
			}
			if rows == nil || len(rows) != len(tc.want) {
				t.Fatalf("rows=%s", out)
			}
			got := map[string]bool{}
			for _, row := range rows {
				got[row["key"].(string)] = true
				if row["source"] == nil || row["label"] == nil {
					t.Fatalf("missing metadata: %v", row)
				}
			}
			for _, key := range tc.want {
				if !got[key] {
					t.Fatalf("missing %s in %s", key, out)
				}
			}
		})
	}
	out, err := executeFeature(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Board view", "workspace_locked", "Calendar", "SOURCE", "LOCKED"} {
		if !strings.Contains(out, part) {
			t.Fatalf("missing %q: %s", part, out)
		}
	}
}

func TestFeatureGetIncludesDefinitionResolutionAndOverrides(t *testing.T) {
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(featureFixture)) })
	out, err := executeFeature(t, "get", "plugin:calendar:week")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Definition map[string]any `json:"definition"`
		Value      bool           `json:"value"`
		Source     string         `json:"source"`
		Locked     bool           `json:"locked"`
		Overrides  map[string]any `json:"overrides"`
	}
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Definition["parent"] != "board" || got.Source != "workspace_locked" || got.Value || !got.Locked || got.Overrides["workspace"] == nil {
		t.Fatalf("get=%s", out)
	}
	if _, err = executeFeature(t, "get", "unknown"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown key error=%v", err)
	}
}

func TestFeatureMutationsUseExplicitScopeAndExactBody(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		method, path string
		body         map[string]any
	}{
		{"workspace set", []string{"set", "board", "off", "--workspace", "--locked", "--reason", "Rollout paused"}, "PUT", "/board/workspace", map[string]any{"enabled": false, "locked": true, "reason": "Rollout paused"}},
		{"personal set", []string{"set", "board", "on"}, "PUT", "/board", map[string]any{"enabled": true}},
		{"workspace unset", []string{"unset", "board", "--workspace"}, "DELETE", "/board/workspace", nil},
		{"personal unset", []string{"unset", "board"}, "DELETE", "/board", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != "/api/workspaces/ws-1/feature-flags"+tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if tc.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					want, _ := json.Marshal(tc.body)
					got, _ := json.Marshal(body)
					if string(want) != string(got) {
						t.Errorf("body=%s want=%s", got, want)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})
			out, err := executeFeature(t, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(out, "board") {
				t.Fatalf("calls=%d output=%s", calls, out)
			}
		})
	}
}

func TestFeatureRejectsInvalidArgumentsWithoutRequests(t *testing.T) {
	calls := 0
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) })
	for _, args := range [][]string{
		{"set", "board", "true"}, {"set", "board", "on", "--locked"},
		{"set", "board", "on", "--reason", "ignored"}, {"set", "board", "on", "--personal"},
		{"list", "--state", "true"}, {"list", "--output", "yaml"},
		{"set", "board"}, {"unset"}, {"get"}, {"list", "extra"}, {"history", "board", "--limit", "0"},
	} {
		if _, err := executeFeature(t, args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid commands made %d requests", calls)
	}
}

func TestFeatureHistoryPreservesAuditAndCursor(t *testing.T) {
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspaces/ws-1/feature-flags" {
			_, _ = w.Write([]byte(featureFixture))
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/workspaces/ws-1/feature-flags/audit" || r.URL.Query().Get("key") != "plugin:calendar:week" || r.URL.Query().Get("limit") != "12" || r.URL.Query().Get("before") != "2026-10-09T09:00:00Z" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"entries":[{"id":"audit-1","flag_key":"plugin:calendar:week","scope_type":"workspace","action":"set","old_enabled":null,"new_enabled":false,"new_locked":true,"actor_type":"user","actor_id":"user-1","reason":"Rollout paused","created_at":"2026-10-08T09:00:00Z"}],"next_before":"2026-10-08T09:00:00Z"}`))
	})
	out, err := executeFeature(t, "history", "plugin:calendar:week", "--limit", "12", "--before", "2026-10-09T09:00:00Z", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	current, _ := got["current"].(map[string]any)
	if got["next_before"] != "2026-10-08T09:00:00Z" || !strings.Contains(out, `"old_enabled": null`) || !strings.Contains(out, "Rollout paused") || current["source"] != "workspace_locked" || current["locked"] != true {
		t.Fatalf("history=%s", out)
	}
}

func TestFeatureHistoryShowsEffectiveSource(t *testing.T) {
	for _, source := range []string{"parent:board", "deployment", "workspace_locked", "member", "workspace", "rule", "default", "frozen"} {
		t.Run(source, func(t *testing.T) {
			var paths []string
			featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
				if r.URL.Path == "/api/workspaces/ws-1/feature-flags/audit" {
					_, _ = w.Write([]byte(`{"entries":[{"scope_type":"workspace","action":"set","new_enabled":false,"actor_type":"user","actor_id":"user-1","created_at":"2026-10-08T09:00:00Z"}]}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"definitions": []map[string]any{{"key": "board", "label": "Board"}},
					"resolved":    map[string]any{"board": map[string]any{"value": false, "source": source, "locked": false}},
				})
			})
			out, err := executeFeature(t, "history", "board")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Source: "+source) || !strings.Contains(out, "workspace") || len(paths) != 2 || !strings.Contains(paths[1], "/audit?key=board") {
				t.Fatalf("paths=%v output=%s", paths, out)
			}
		})
	}
}

func TestFeatureServerErrorsAreNotSuccess(t *testing.T) {
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"admin required"}`, http.StatusForbidden)
	})
	for _, args := range [][]string{{"list"}, {"get", "board"}, {"set", "board", "on"}, {"unset", "board"}, {"history", "board"}} {
		out, err := executeFeature(t, args...)
		var httpErr *cli.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != 403 || strings.TrimSpace(out) != "" {
			t.Fatalf("%v: output=%s error=%v", args, out, err)
		}
	}
}

func TestFeatureUsesWorkspaceFlagAndRejectsMissingWorkspace(t *testing.T) {
	featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/other/feature-flags" || r.Header.Get("X-Workspace-ID") != "other" {
			t.Errorf("request=%s headers=%v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(featureFixture))
	})
	if _, err := executeFeature(t, "list", "--workspace-id", "other"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	if _, err := executeFeature(t, "list"); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("missing workspace error=%v", err)
	}
}

func TestFeatureRejectsMalformedSnapshots(t *testing.T) {
	for _, body := range []string{`{}`, `{"definitions":[{"key":"board"}],"resolved":{}}`, `{"definitions":[{"key":"board"}],"resolved":{"board":{"source":"default"}}}`} {
		t.Run(body, func(t *testing.T) {
			featureTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			out, err := executeFeature(t, "list", "--output", "json")
			if err == nil || !strings.Contains(err.Error(), "invalid feature flags response") || strings.TrimSpace(out) != "" {
				t.Fatalf("output=%s error=%v", out, err)
			}
		})
	}
}

func TestFeaturePreservesServerDefinedSourcesAndMetadata(t *testing.T) {
	for _, source := range []string{"default", "rule", "workspace", "member", "workspace_locked", "deployment", "frozen", "parent:board", "future_source"} {
		t.Run(source, func(t *testing.T) {
			featureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"definitions": []map[string]any{{"key": "server_defined", "label": "New feature", "group": "new", "default": true, "scope_policy": "workspace", "future_metadata": "retained"}},
					"resolved":    map[string]any{"server_defined": map[string]any{"value": false, "source": source, "locked": true}},
					"overrides":   map[string]any{"workspace": map[string]any{}, "member": map[string]any{}},
				})
			})
			out, err := executeFeature(t, "get", "server_defined")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err = json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			definition := got["definition"].(map[string]any)
			if got["source"] != source || got["value"] != false || definition["future_metadata"] != "retained" {
				t.Fatalf("get=%s", out)
			}
		})
	}
}
