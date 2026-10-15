package cli

// Bank configuration commands: the resolved view, the override writer, the
// reset and the disposition shortcut.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// registerBankConfigGroup wires the configuration commands.
func registerBankConfigGroup(bank *SubCmd) {
	config := bank.Sub("config", "show bank configuration")
	overridesOnly := config.Flags().Bool("overrides-only", false, "show only the bank-specific overrides")
	config.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank config", d.Args, 1); err != nil {
			return err
		}
		return bankConfigGet(ctx, d, d.Args[0], *overridesOnly)
	})

	setConfig := bank.Sub("set-config", "set bank configuration overrides")
	cf := bindBankConfigFlags(setConfig)
	setConfig.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank set-config", d.Args, 1); err != nil {
			return err
		}
		updates, err := cf.updates(givenFlags(d.Flags))
		if err != nil {
			return err
		}
		return bankConfigSet(ctx, d, d.Args[0], updates)
	})

	resetConfig := bank.Sub("reset-config", "reset bank configuration overrides")
	resetYes := resetConfig.Flags().Bool("yes", false, "skip the confirmation prompt")
	resetConfig.Flags().BoolVar(resetYes, "y", false, "skip the confirmation prompt (short)")
	resetConfig.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank reset-config", d.Args, 1); err != nil {
			return err
		}
		return bankConfigReset(ctx, d, d.Args[0], *resetYes)
	})

	setDisp := bank.Sub("set-disposition", "set the disposition traits")
	skepticism := setDisp.Flags().Int("skepticism", 0, "skepticism trait (1-5, required)")
	literalism := setDisp.Flags().Int("literalism", 0, "literalism trait (1-5, required)")
	empathy := setDisp.Flags().Int("empathy", 0, "empathy trait (1-5, required)")
	setDisp.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank set-disposition", d.Args, 1); err != nil {
			return err
		}
		given := givenFlags(d.Flags)
		if !given["skepticism"] || !given["literalism"] || !given["empathy"] {
			return NewUsageError("bank set-disposition requires --skepticism, --literalism, and --empathy")
		}
		traits := traitFlags{skepticism: *skepticism, literalism: *literalism, empathy: *empathy}
		for _, t := range []struct {
			flag  string
			value int
		}{
			{"--skepticism", traits.skepticism},
			{"--literalism", traits.literalism},
			{"--empathy", traits.empathy},
		} {
			if err := validateTraitRange(t.flag, t.value); err != nil {
				return err
			}
		}
		return bankSetDisposition(ctx, d, d.Args[0], traits)
	})
}

// bankConfigFlags carries the set-config flag variables, read at action
// time.
type bankConfigFlags struct {
	llmProvider               *string
	llmModel                  *string
	llmAPIKey                 *string
	llmBaseURL                *string
	retainMission             *string
	retainExtractionMode      *string
	retainChunkSize           *int
	retainStructuredChunkSize *int
	observationsMission       *string
	reflectMission            *string
	skepticism                *int
	literalism                *int
	empathy                   *int
}

// bindBankConfigFlags registers the set-config flag set on cmd.
func bindBankConfigFlags(cmd *SubCmd) *bankConfigFlags {
	f := cmd.Flags()
	return &bankConfigFlags{
		llmProvider:               f.String("llm-provider", "", "LLM provider"),
		llmModel:                  f.String("llm-model", "", "LLM model"),
		llmAPIKey:                 f.String("llm-api-key", "", "LLM API key"),
		llmBaseURL:                f.String("llm-base-url", "", "LLM base URL"),
		retainMission:             f.String("retain-mission", "", "retain mission"),
		retainExtractionMode:      f.String("retain-extraction-mode", "", "extraction mode: concise, verbose, custom, verbatim, chunks"),
		retainChunkSize:           f.Int("retain-chunk-size", 0, "chunk size (>= 1)"),
		retainStructuredChunkSize: f.Int("retain-structured-chunk-size", 0, "structured chunk size (>= 1)"),
		observationsMission:       f.String("observations-mission", "", "observations mission"),
		reflectMission:            f.String("reflect-mission", "", "reflection mission"),
		skepticism:                f.Int("disposition-skepticism", 0, "skepticism trait (1-5)"),
		literalism:                f.Int("disposition-literalism", 0, "literalism trait (1-5)"),
		empathy:                   f.Int("disposition-empathy", 0, "empathy trait (1-5)"),
	}
}

// updates builds the `updates` object from the flags that were given,
// mapping each flag onto its snake_case config key. It enforces the
// client-side rules: at least one flag, trait values 1-5 and chunk sizes
// >= 1.
func (c *bankConfigFlags) updates(given map[string]bool) (map[string]any, error) {
	updates := map[string]any{}
	addString := func(flag, key string, value *string) {
		if given[flag] {
			updates[key] = *value
		}
	}
	addInt := func(flag, key string, value *int, min int) error {
		if !given[flag] {
			return nil
		}
		if *value < min {
			return NewUsageError("--%s must be at least %d", flag, min)
		}
		updates[key] = *value
		return nil
	}
	addTrait := func(flag, key string, value *int) error {
		if !given[flag] {
			return nil
		}
		if err := validateTraitRange("--"+flag, *value); err != nil {
			return err
		}
		updates[key] = *value
		return nil
	}
	addString("llm-provider", "llm_provider", c.llmProvider)
	addString("llm-model", "llm_model", c.llmModel)
	addString("llm-api-key", "llm_api_key", c.llmAPIKey)
	addString("llm-base-url", "llm_base_url", c.llmBaseURL)
	addString("retain-mission", "retain_mission", c.retainMission)
	addString("retain-extraction-mode", "retain_extraction_mode", c.retainExtractionMode)
	if err := addInt("retain-chunk-size", "retain_chunk_size", c.retainChunkSize, 1); err != nil {
		return nil, err
	}
	if err := addInt("retain-structured-chunk-size", "retain_structured_chunk_size", c.retainStructuredChunkSize, 1); err != nil {
		return nil, err
	}
	addString("observations-mission", "observations_mission", c.observationsMission)
	addString("reflect-mission", "reflect_mission", c.reflectMission)
	if err := addTrait("disposition-skepticism", "disposition_skepticism", c.skepticism); err != nil {
		return nil, err
	}
	if err := addTrait("disposition-literalism", "disposition_literalism", c.literalism); err != nil {
		return nil, err
	}
	if err := addTrait("disposition-empathy", "disposition_empathy", c.empathy); err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, NewCommandError("No config updates provided. Use --llm-provider, --llm-model, --retain-mission, --retain-chunk-size, --observations-mission, or other flags")
	}
	return updates, nil
}

// bankConfigGet implements `bank config`. With --overrides-only the machine
// output is just the overrides object; otherwise the full response.
func bankConfigGet(ctx context.Context, d *Deps, bankID string, overridesOnly bool) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/config"), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		if overridesOnly {
			var resp BankConfigResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return &DecodeError{Reason: err.Error(), Body: string(raw)}
			}
			if resp.Overrides == nil {
				resp.Overrides = map[string]any{}
			}
			return d.Format.Render(d.UI.Stdout, resp.Overrides)
		}
		return renderRaw(d, raw)
	}
	var resp BankConfigResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Infof("Configuration for bank '%s'", bankID)
	ui.Plain("")
	if overridesOnly {
		ui.Plain("Bank-specific overrides:")
		if len(resp.Overrides) == 0 {
			ui.Plain("  (none - using defaults)")
			return nil
		}
		renderConfigEntries(ui, resp.Overrides)
		return nil
	}
	ui.Plain("Resolved configuration (with all overrides applied):")
	renderConfigEntries(ui, resp.Config)
	return nil
}

// renderConfigEntries prints sorted `  <key>: <value>` lines, the value as a
// debug-formatted literal with strings quoted.
func renderConfigEntries(ui *UI, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ui.Plain("  %s: %#v", k, m[k])
	}
}

// bankConfigSet implements `bank set-config`.
func bankConfigSet(ctx context.Context, d *Deps, bankID string, updates map[string]any) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, "/config"), map[string]any{"updates": updates}, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp BankConfigResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	d.UI.Infof("Configuration updated for bank '%s'", bankID)
	d.UI.Plain("")
	d.UI.Plain("Updated overrides:")
	renderConfigEntries(d.UI, resp.Overrides)
	return nil
}

// bankConfigReset implements `bank reset-config`.
func bankConfigReset(ctx context.Context, d *Deps, bankID string, yes bool) error {
	prompt := fmt.Sprintf("Reset all configuration overrides for bank '%s'?", bankID)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, "/config"), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	d.UI.Infof("Configuration reset to defaults for bank '%s'", bankID)
	return nil
}

// bankSetDisposition writes the three trait overrides, then re-reads the
// disposition exactly like `bank disposition`.
func bankSetDisposition(ctx context.Context, d *Deps, bankID string, traits traitFlags) error {
	updates := map[string]any{
		"disposition_skepticism": traits.skepticism,
		"disposition_literalism": traits.literalism,
		"disposition_empathy":    traits.empathy,
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, "/config"), map[string]any{"updates": updates}, &raw); err != nil {
		return err
	}
	v, err := readDisposition(ctx, d, bankID)
	if err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, v)
	}
	d.UI.Infof("Disposition updated for bank '%s'", bankID)
	renderDispositionView(d.UI, v)
	return nil
}
