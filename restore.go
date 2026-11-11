package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// newUUIDv4 returns a random RFC 4122 v4 UUID. Used when appending options to
// SELECT/MULTI_SELECT fields, where source-side ids may collide with target's.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const batchSize = 60

var duplicatePattern = regexp.MustCompile(`(?i)already exists|already used|duplicate|conflict|not_available|not available`)

var http500Pattern = regexp.MustCompile(`HTTP 5\d\d`)

func containsHTTP500(s string) bool { return http500Pattern.MatchString(s) }

// perObjectReadOnly lists fields that the REST endpoint computes server-side
// for specific standard objects and refuses on write. Keyed by nameSingular.
var perObjectReadOnly = map[string][]string{
	"workflow": {"statuses", "lastPublishedVersionId"},
}

// systemObjectsWithRecords names system objects (isSystem == true) whose records
// must still be backed up and restored. A workflow's trigger and steps live in
// its workflowVersion records, not on the workflow row; without them a restored
// workflow is an empty shell. Keyed by nameSingular. (workflowRun execution
// history is intentionally excluded — not needed to reconstruct a workflow.)
var systemObjectsWithRecords = map[string]struct{}{
	"workflowVersion": {},
}

// includeObjectRecords reports whether an object's records participate in
// backup/restore: it must be active, and either non-system or explicitly
// allowlisted in systemObjectsWithRecords. Single source of truth shared by the
// backup record fetch (backup.go) and restoreRecords.
func includeObjectRecords(obj ObjectMetadata) bool {
	if !obj.IsActive {
		return false
	}
	if obj.IsSystem {
		_, ok := systemObjectsWithRecords[obj.NameSingular]
		return ok
	}
	return true
}

// readOnlyFieldNames returns field names that the REST batch endpoint refuses
// to accept on write: TS_VECTOR types (e.g. searchVector), plus a hardcoded
// list of computed/derived standard fields per object (see perObjectReadOnly).
func readOnlyFieldNames(obj ObjectMetadata) map[string]struct{} {
	out := make(map[string]struct{})
	for _, f := range obj.Fields {
		if f.Type == "TS_VECTOR" {
			out[f.Name] = struct{}{}
		}
	}
	for _, name := range perObjectReadOnly[obj.NameSingular] {
		out[name] = struct{}{}
	}
	return out
}

func stripFields(rows []RecordPayload, drop map[string]struct{}) []RecordPayload {
	if len(drop) == 0 {
		return rows
	}
	out := make([]RecordPayload, len(rows))
	for i, r := range rows {
		nr := make(RecordPayload, len(r))
		for k, v := range r {
			if _, skip := drop[k]; skip {
				continue
			}
			nr[k] = v
		}
		out[i] = nr
	}
	return out
}

func restoreCustomObjects(client *TwentyClient, objects []ObjectMetadata) error {
	var custom []ObjectMetadata
	for _, o := range objects {
		if o.IsCustom && o.IsActive && !o.IsSystem {
			custom = append(custom, o)
		}
	}
	if len(custom) == 0 {
		return nil
	}

	fmt.Printf("\nCreating %d custom object(s)…\n", len(custom))

	for _, obj := range custom {
		input := map[string]any{
			"nameSingular":  obj.NameSingular,
			"namePlural":    obj.NamePlural,
			"labelSingular": obj.LabelSingular,
			"labelPlural":   obj.LabelPlural,
		}
		if obj.Description != nil {
			input["description"] = *obj.Description
		}
		if obj.Icon != nil {
			input["icon"] = *obj.Icon
		}
		err := client.MetaGraphQL(`
      mutation CreateOneObject($input: CreateOneObjectInput!) {
        createOneObject(input: $input) { id nameSingular }
      }
    `, map[string]any{"input": map[string]any{"object": input}}, nil)
		if err != nil {
			if duplicatePattern.MatchString(err.Error()) {
				fmt.Printf("  = %s (already exists)\n", obj.NameSingular)
				continue
			}
			return fmt.Errorf("failed to create object %q: %w", obj.NameSingular, err)
		}
		fmt.Printf("  + %s\n", obj.NameSingular)
	}
	return nil
}

// fetchObjectIDs returns nameSingular → metadata id on the target instance.
func fetchObjectIDs(client *TwentyClient) (map[string]string, error) {
	var meta struct {
		Objects struct {
			Edges []struct {
				Node struct {
					ID           string `json:"id"`
					NameSingular string `json:"nameSingular"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"objects"`
	}
	if err := client.MetaGraphQL(
		`query { objects(paging: { first: 1000 }) { edges { node { id nameSingular } } } }`,
		nil, &meta,
	); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(meta.Objects.Edges))
	for _, e := range meta.Objects.Edges {
		out[e.Node.NameSingular] = e.Node.ID
	}
	return out, nil
}

func restoreCustomFields(client *TwentyClient, objects []ObjectMetadata) error {
	idByName, err := fetchObjectIDs(client)
	if err != nil {
		return err
	}

	type pair struct {
		obj   ObjectMetadata
		field FieldMetadata
	}
	var pairs []pair
	for _, obj := range objects {
		if !obj.IsActive || obj.IsSystem {
			continue
		}
		for _, f := range obj.Fields {
			if f.IsCustom && f.IsActive && !f.IsSystem && f.Type != "RELATION" {
				pairs = append(pairs, pair{obj, f})
			}
		}
	}

	if len(pairs) == 0 {
		return nil
	}
	fmt.Printf("\nCreating %d custom field(s)…\n", len(pairs))

	for _, p := range pairs {
		objectMetadataID, ok := idByName[p.obj.NameSingular]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! %s.%s — object not found on target, skipping\n",
				p.obj.NameSingular, p.field.Name)
			continue
		}

		input := map[string]any{
			"objectMetadataId": objectMetadataID,
			"name":             p.field.Name,
			"label":            p.field.Label,
			"type":             p.field.Type,
			"isNullable":       p.field.IsNullable,
		}
		if p.field.Description != nil {
			input["description"] = *p.field.Description
		}
		if p.field.Icon != nil {
			input["icon"] = *p.field.Icon
		}
		if len(p.field.DefaultValue) > 0 {
			input["defaultValue"] = p.field.DefaultValue
		}
		if len(p.field.Options) > 0 {
			input["options"] = p.field.Options
		}
		if len(p.field.Settings) > 0 {
			input["settings"] = p.field.Settings
		}

		err := client.MetaGraphQL(`
      mutation CreateOneField($input: CreateOneFieldMetadataInput!) {
        createOneField(input: $input) { id name }
      }
    `, map[string]any{"input": map[string]any{"field": input}}, nil)
		if err != nil {
			if duplicatePattern.MatchString(err.Error()) {
				fmt.Printf("  = %s.%s (already exists)\n", p.obj.NameSingular, p.field.Name)
			} else {
				fmt.Fprintf(os.Stderr, "  ! %s.%s: %s\n", p.obj.NameSingular, p.field.Name, err)
			}
			continue
		}
		fmt.Printf("  + %s.%s\n", p.obj.NameSingular, p.field.Name)
	}
	return nil
}

// restoreCustomRelations creates custom RELATION fields after objects and
// scalar fields exist. Only MANY_TO_ONE is created explicitly; the
// ONE_TO_MANY inverse on the target object is auto-created by Twenty.
func restoreCustomRelations(client *TwentyClient, objects []ObjectMetadata) error {
	idByName, err := fetchObjectIDs(client)
	if err != nil {
		return err
	}

	type relPair struct {
		obj   ObjectMetadata
		field FieldMetadata
	}
	var pairs []relPair
	for _, obj := range objects {
		if !obj.IsActive || obj.IsSystem {
			continue
		}
		for _, f := range obj.Fields {
			if !f.IsCustom || !f.IsActive || f.IsSystem || f.Type != "RELATION" {
				continue
			}
			if f.Relation == nil || f.Relation.Type != "MANY_TO_ONE" {
				continue
			}
			pairs = append(pairs, relPair{obj, f})
		}
	}
	if len(pairs) == 0 {
		return nil
	}

	type fieldKey struct{ obj, field string }
	inverse := make(map[fieldKey]FieldMetadata)
	for _, o := range objects {
		for _, f := range o.Fields {
			inverse[fieldKey{o.NameSingular, f.Name}] = f
		}
	}

	fmt.Printf("\nCreating %d custom relation(s)…\n", len(pairs))

	for _, p := range pairs {
		objectMetadataID, ok := idByName[p.obj.NameSingular]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! %s.%s — object not found on target, skipping\n",
				p.obj.NameSingular, p.field.Name)
			continue
		}
		targetName := p.field.Relation.TargetObjectMetadata.NameSingular
		targetID, ok := idByName[targetName]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! %s.%s — target object %q not found on target, skipping\n",
				p.obj.NameSingular, p.field.Name, targetName)
			continue
		}

		invName := p.field.Relation.TargetFieldMetadata.Name
		inv := inverse[fieldKey{targetName, invName}]
		targetFieldLabel := invName
		if inv.Label != "" {
			targetFieldLabel = inv.Label
		}
		targetFieldIcon := "IconLink"
		if inv.Icon != nil && *inv.Icon != "" {
			targetFieldIcon = *inv.Icon
		}

		input := map[string]any{
			"objectMetadataId": objectMetadataID,
			"name":             p.field.Name,
			"label":            p.field.Label,
			"type":             "RELATION",
			"isNullable":       p.field.IsNullable,
			"relationCreationPayload": map[string]any{
				"type":                   "MANY_TO_ONE",
				"targetObjectMetadataId": targetID,
				"targetFieldLabel":       targetFieldLabel,
				"targetFieldIcon":        targetFieldIcon,
			},
		}
		if p.field.Description != nil {
			input["description"] = *p.field.Description
		}
		if p.field.Icon != nil {
			input["icon"] = *p.field.Icon
		}

		err := client.MetaGraphQL(`
      mutation CreateOneField($input: CreateOneFieldMetadataInput!) {
        createOneField(input: $input) { id name }
      }
    `, map[string]any{"input": map[string]any{"field": input}}, nil)
		if err != nil {
			if duplicatePattern.MatchString(err.Error()) {
				fmt.Printf("  = %s.%s → %s (already exists)\n", p.obj.NameSingular, p.field.Name, targetName)
				continue
			}
			fmt.Fprintf(os.Stderr, "  ! %s.%s → %s: %s\n", p.obj.NameSingular, p.field.Name, targetName, err)
			continue
		}
		fmt.Printf("  + %s.%s → %s\n", p.obj.NameSingular, p.field.Name, targetName)
	}
	return nil
}

type selectOption struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Color    string  `json:"color"`
	Value    string  `json:"value"`
	Position float64 `json:"position"`
}

type targetField struct {
	ID      string
	Type    string
	Options json.RawMessage
}

// fetchTargetFields returns nested map[objectNameSingular][fieldName] →
// target-instance field id/type/options. Used to sync SELECT options.
func fetchTargetFields(client *TwentyClient) (map[string]map[string]targetField, error) {
	var meta struct {
		Objects struct {
			Edges []struct {
				Node struct {
					NameSingular string `json:"nameSingular"`
					Fields       struct {
						Edges []struct {
							Node struct {
								ID      string          `json:"id"`
								Name    string          `json:"name"`
								Type    string          `json:"type"`
								Options json.RawMessage `json:"options"`
							} `json:"node"`
						} `json:"edges"`
					} `json:"fields"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"objects"`
	}
	q := `query {
      objects(paging: { first: 1000 }) {
        edges { node {
          nameSingular
          fields(paging: { first: 1000 }) { edges { node { id name type options } } }
        } }
      }
    }`
	if err := client.MetaGraphQL(q, nil, &meta); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]targetField, len(meta.Objects.Edges))
	for _, o := range meta.Objects.Edges {
		m := make(map[string]targetField, len(o.Node.Fields.Edges))
		for _, fe := range o.Node.Fields.Edges {
			m[fe.Node.Name] = targetField{ID: fe.Node.ID, Type: fe.Node.Type, Options: fe.Node.Options}
		}
		out[o.Node.NameSingular] = m
	}
	return out, nil
}

// syncFieldOptions adds SELECT/MULTI_SELECT options present in the backup but
// missing on the target. Target options are kept intact (their IDs may be
// referenced by existing records on the target); only new entries are
// appended via updateOneField with fresh UUIDs to avoid clashes with the
// target's seeded option ids.
func syncFieldOptions(client *TwentyClient, objects []ObjectMetadata) error {
	target, err := fetchTargetFields(client)
	if err != nil {
		return err
	}

	var added int
	for _, obj := range objects {
		if !obj.IsActive || obj.IsSystem {
			continue
		}
		tfields, ok := target[obj.NameSingular]
		if !ok {
			continue
		}
		for _, f := range obj.Fields {
			if f.Type != "SELECT" && f.Type != "MULTI_SELECT" {
				continue
			}
			if len(f.Options) == 0 {
				continue
			}
			tf, ok := tfields[f.Name]
			if !ok {
				continue
			}
			var src, dst []selectOption
			if err := json.Unmarshal(f.Options, &src); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s.%s options parse (source): %s\n", obj.NameSingular, f.Name, err)
				continue
			}
			if len(tf.Options) > 0 && string(tf.Options) != "null" {
				if err := json.Unmarshal(tf.Options, &dst); err != nil {
					fmt.Fprintf(os.Stderr, "  ! %s.%s options parse (target): %s\n", obj.NameSingular, f.Name, err)
					continue
				}
			}
			have := make(map[string]struct{}, len(dst))
			for _, o := range dst {
				have[o.Value] = struct{}{}
			}
			usedID := make(map[string]struct{}, len(dst))
			for _, o := range dst {
				usedID[o.ID] = struct{}{}
			}
			var missing []selectOption
			maxPos := 0.0
			for _, o := range dst {
				if o.Position > maxPos {
					maxPos = o.Position
				}
			}
			for _, o := range src {
				if _, exists := have[o.Value]; exists {
					continue
				}
				maxPos++
				o.Position = maxPos
				if _, clash := usedID[o.ID]; clash || o.ID == "" {
					o.ID = newUUIDv4()
				}
				usedID[o.ID] = struct{}{}
				missing = append(missing, o)
			}
			if len(missing) == 0 {
				continue
			}
			merged := append(dst, missing...)
			mergedJSON, err := json.Marshal(merged)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s.%s marshal merged options: %s\n", obj.NameSingular, f.Name, err)
				continue
			}

			if added == 0 {
				fmt.Println("\nSyncing field options…")
			}
			added++

			mutErr := client.MetaGraphQL(`
        mutation UpdateOneField($input: UpdateOneFieldMetadataInput!) {
          updateOneField(input: $input) { id name }
        }
      `, map[string]any{
				"input": map[string]any{
					"id":     tf.ID,
					"update": map[string]any{"options": json.RawMessage(mergedJSON)},
				},
			}, nil)
			if mutErr != nil {
				fmt.Fprintf(os.Stderr, "  ! %s.%s: %s\n", obj.NameSingular, f.Name, mutErr)
				continue
			}
			vals := make([]string, len(missing))
			for i, o := range missing {
				vals[i] = o.Value
			}
			fmt.Printf("  + %s.%s adds %v\n", obj.NameSingular, f.Name, vals)
		}
	}
	return nil
}

// topoSort returns objects in dependency order (parents before children).
// Back-edges in cycles (including self-references) are broken implicitly:
// visit() is re-entrant-safe via inStack.
func topoSort(objects []ObjectMetadata) []ObjectMetadata {
	byPlural := make(map[string]ObjectMetadata, len(objects))
	for _, o := range objects {
		byPlural[o.NamePlural] = o
	}

	deps := make(map[string]map[string]struct{}, len(objects))
	for _, obj := range objects {
		d := make(map[string]struct{})
		for _, f := range obj.Fields {
			if f.Type != "RELATION" || f.Relation == nil {
				continue
			}
			if f.Relation.Type != "MANY_TO_ONE" {
				continue
			}
			targetName := f.Relation.TargetObjectMetadata.NameSingular
			if targetName == obj.NameSingular {
				continue // self-reference
			}
			for _, other := range objects {
				if other.NameSingular == targetName {
					d[other.NamePlural] = struct{}{}
					break
				}
			}
		}
		deps[obj.NamePlural] = d
	}

	var order []string
	done := make(map[string]bool)
	inStack := make(map[string]bool)

	var visit func(name string)
	visit = func(name string) {
		if done[name] || inStack[name] {
			return
		}
		inStack[name] = true
		for dep := range deps[name] {
			visit(dep)
		}
		delete(inStack, name)
		done[name] = true
		order = append(order, name)
	}

	for _, obj := range objects {
		visit(obj.NamePlural)
	}

	result := make([]ObjectMetadata, 0, len(order))
	for _, n := range order {
		if o, ok := byPlural[n]; ok {
			result = append(result, o)
		}
	}
	return result
}

func restoreRecords(client *TwentyClient, backup *BackupFile) error {
	var active []ObjectMetadata
	for _, o := range backup.Objects {
		if includeObjectRecords(o) {
			active = append(active, o)
		}
	}
	sorted := topoSort(active)

	fmt.Println("\nRestoring records…")

	for _, obj := range sorted {
		rows := backup.Records[obj.NamePlural]
		if len(rows) == 0 {
			continue
		}
		fmt.Printf("  %s: %d record(s)\n", obj.NamePlural, len(rows))

		readOnly := readOnlyFieldNames(obj)

		ok, fail := 0, 0
		for i := 0; i < len(rows); i += batchSize {
			end := i + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			batch := stripFields(rows[i:end], readOnly)

			body, err := json.Marshal(batch)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    batch %d marshal failed: %s\n", i/batchSize+1, err)
				fail += len(batch)
				continue
			}
			if err := client.Post("/rest/batch/"+obj.NamePlural+"?upsert=true", body, nil); err == nil {
				ok += len(batch)
				continue
			} else {
				fmt.Fprintf(os.Stderr, "    batch %d failed, falling back to per-record: %s\n", i/batchSize+1, err)
			}

			first500Logged := false
			for _, row := range batch {
				rb, mErr := json.Marshal(row)
				if mErr != nil {
					fmt.Fprintf(os.Stderr, "      record marshal failed: %s\n", mErr)
					fail++
					continue
				}
				if pErr := client.Post("/rest/"+obj.NamePlural+"?upsert=true", rb, nil); pErr != nil {
					id, _ := row["id"].(string)
					fmt.Fprintf(os.Stderr, "      ! id=%s: %s\n", id, pErr)
					if !first500Logged && containsHTTP500(pErr.Error()) {
						fmt.Fprintf(os.Stderr, "      body: %s\n", string(rb))
						first500Logged = true
					}
					fail++
					continue
				}
				ok++
			}
		}

		if fail > 0 {
			fmt.Printf("    %d ok, %d failed\n", ok, fail)
		} else {
			fmt.Printf("    %d ok\n", ok)
		}
	}
	return nil
}

// workflowIsActive reports whether a workflow record's statuses array contains
// "ACTIVE" (statuses is a MULTI_SELECT, decoded as []any of strings).
func workflowIsActive(r RecordPayload) bool {
	statuses, ok := r["statuses"].([]any)
	if !ok {
		return false
	}
	for _, s := range statuses {
		if str, ok := s.(string); ok && str == "ACTIVE" {
			return true
		}
	}
	return false
}

// republishWorkflows re-activates each workflow that was ACTIVE on the source by
// calling the core GraphQL activateWorkflowVersion mutation on its published
// version. That single call does what the REST record restore cannot: it sets
// the workflow's lastPublishedVersionId + statuses (both read-only on write) and
// registers the automated trigger (cron / DB-event) server-side. Copying a
// workflowAutomatedTrigger record instead would not register the cron and would
// duplicate the trigger once activation runs, so activation is the only correct
// path.
//
// The mutation is guarded by UserAuthGuard, which rejects workspace API keys
// (request.user is undefined). This pass is therefore best-effort: every failure
// is collected and the workflow is printed for manual re-activation in the UI.
func republishWorkflows(client *TwentyClient, backup *BackupFile) {
	plural := "workflows"
	for _, o := range backup.Objects {
		if o.NameSingular == "workflow" {
			plural = o.NamePlural
			break
		}
	}
	rows := backup.Records[plural]
	if len(rows) == 0 {
		return
	}

	type wf struct{ name, versionID string }
	var toActivate []wf
	for _, r := range rows {
		if !workflowIsActive(r) {
			continue
		}
		vid, _ := r["lastPublishedVersionId"].(string)
		if vid == "" {
			continue // never published; restored as draft
		}
		name, _ := r["name"].(string)
		toActivate = append(toActivate, wf{name: name, versionID: vid})
	}
	if len(toActivate) == 0 {
		return
	}

	fmt.Printf("\nRe-activating %d workflow(s)…\n", len(toActivate))
	const mutation = `mutation Activate($id: UUID!) { activateWorkflowVersion(workflowVersionId: $id) }`

	var manual []wf
	for _, w := range toActivate {
		if err := client.GraphQL(mutation, map[string]any{"id": w.versionID}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %s\n", w.name, err)
			manual = append(manual, w)
			continue
		}
		fmt.Printf("  ✓ %s\n", w.name)
	}

	if len(manual) > 0 {
		fmt.Printf("\n%d workflow(s) could not be auto-activated. activateWorkflowVersion\n", len(manual))
		fmt.Println("requires a logged-in user token; the workspace API key is rejected by")
		fmt.Println("UserAuthGuard. Re-activate each in the Twenty UI (open the workflow → Activate):")
		for _, w := range manual {
			fmt.Printf("  - %s (version %s)\n", w.name, w.versionID)
		}
	}
}

func RunRestore(client *TwentyClient, backupPath string, dryRun bool) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	var backup BackupFile
	if err := json.Unmarshal(data, &backup); err != nil {
		return fmt.Errorf("parsing backup: %w", err)
	}

	total := 0
	for _, rows := range backup.Records {
		total += len(rows)
	}
	fmt.Printf("Backup date : %s\n", backup.CreatedAt)
	fmt.Printf("Format ver  : %s\n", backup.Version)
	fmt.Printf("Objects     : %d\n", len(backup.Objects))
	fmt.Printf("Records     : %d\n", total)

	if dryRun {
		fmt.Println("\n[dry-run] No changes made. Would restore:")
		for name, rows := range backup.Records {
			if len(rows) > 0 {
				fmt.Printf("  %s: %d\n", name, len(rows))
			}
		}
		return nil
	}

	if err := restoreCustomObjects(client, backup.Objects); err != nil {
		return err
	}
	if err := restoreCustomFields(client, backup.Objects); err != nil {
		return err
	}
	if err := restoreCustomRelations(client, backup.Objects); err != nil {
		return err
	}
	if err := syncFieldOptions(client, backup.Objects); err != nil {
		return err
	}
	if err := restoreRecords(client, &backup); err != nil {
		return err
	}
	republishWorkflows(client, &backup)

	fmt.Println("\nRestore complete.")
	return nil
}
