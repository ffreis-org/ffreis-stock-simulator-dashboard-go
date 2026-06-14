package main

import (
	"strings"
	"testing"
)

func TestFrontendTabsAndPanelsContract(t *testing.T) {
	t.Parallel()

	templateRaw, err := assetsFS.ReadFile("templates/index.gohtml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	content := string(templateRaw)

	for _, token := range []string{
		"id=\"tab-btn-overview\"",
		"id=\"tab-btn-simulations\"",
		"id=\"tab-overview\"",
		"id=\"tab-simulations\"",
		"id=\"sim-flows-body\"",
		"id=\"sim-compare-body\"",
		"id=\"sim-branch-form\"",
	} {
		if !strings.Contains(content, token) {
			t.Fatalf("template missing %q", token)
		}
	}
}

func TestFrontendScript_SimulationEndpointSurface(t *testing.T) {
	t.Parallel()

	raw, err := assetsFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(raw)

	for _, endpoint := range []string{
		"/api/sim/capabilities",
		"/api/sim/flows",
		"/api/sim/branches",
		"/step_many",
		"/observe",
		"/trace",
	} {
		if !strings.Contains(script, endpoint) {
			t.Fatalf("script missing endpoint fragment %q", endpoint)
		}
	}
}

func TestFrontendScript_PatchModePayloadShape(t *testing.T) {
	t.Parallel()

	raw, err := assetsFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(raw)

	for _, token := range []string{
		"function buildBranchPayload",
		"patch_mode",
		"decision_patch",
		"decisions",
		"target_main_step_index",
		"rollback_steps",
		"source_flow_id",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("script missing patch payload token %q", token)
		}
	}
}

func TestFrontendScript_MultiAltAndDeleteFlowBehaviors(t *testing.T) {
	t.Parallel()

	raw, err := assetsFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(raw)

	for _, token := range []string{
		"selectedAltFlowIds",
		"data-select-alt",
		"globalThis.confirm(`Delete alternative flow",
		"\"DELETE\"",
		"renderComparisonTable",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("script missing behavior token %q", token)
		}
	}
}

func TestFrontendScript_DisabledStateWhenCapabilityOff(t *testing.T) {
	t.Parallel()

	raw, err := assetsFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(raw)

	for _, token := range []string{
		"setSimControlsEnabled(false)",
		"sim-disabled",
		"Simulation branch endpoints are unavailable upstream",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("script missing disabled-state token %q", token)
		}
	}
}
