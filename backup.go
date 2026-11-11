package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"
)

const fetchSchemaQuery = `
  query FetchAllObjects {
    objects(paging: { first: 1000 }) {
      edges {
        node {
          id
          nameSingular
          namePlural
          labelSingular
          labelPlural
          description
          icon
          applicationId
          isActive
          isSystem
          fields(paging: { first: 1000 }) {
            edges {
              node {
                id
                name
                label
                type
                description
                icon
                applicationId
                isNullable
                isSystem
                isActive
                defaultValue
                options
                settings
                relation {
                  type
                  sourceObjectMetadata { id nameSingular }
                  targetObjectMetadata { id nameSingular }
                  sourceFieldMetadata { id name }
                  targetFieldMetadata { id name }
                }
              }
            }
          }
        }
      }
    }
  }
`

type schemaObjectNode struct {
	ID            string  `json:"id"`
	NameSingular  string  `json:"nameSingular"`
	NamePlural    string  `json:"namePlural"`
	LabelSingular string  `json:"labelSingular"`
	LabelPlural   string  `json:"labelPlural"`
	Description   *string `json:"description"`
	Icon          *string `json:"icon"`
	ApplicationID string  `json:"applicationId"`
	IsActive      bool    `json:"isActive"`
	IsSystem      bool    `json:"isSystem"`
	Fields        struct {
		Edges []struct {
			Node schemaFieldNode `json:"node"`
		} `json:"edges"`
	} `json:"fields"`
}

// schemaFieldNode decodes a field node from the metadata query. It embeds the
// persisted FieldMetadata plus applicationId, which is transient (used only to
// derive IsCustom at backup time; Twenty 1.28+ no longer exposes isCustom).
type schemaFieldNode struct {
	FieldMetadata
	ApplicationID string `json:"applicationId"`
}

type schemaResponse struct {
	Objects struct {
		Edges []struct {
			Node schemaObjectNode `json:"node"`
		} `json:"edges"`
	} `json:"objects"`
}

type restListResponse struct {
	Data       map[string][]RecordPayload `json:"data"`
	TotalCount int                        `json:"totalCount"`
	PageInfo   struct {
		HasNextPage     bool    `json:"hasNextPage"`
		HasPreviousPage bool    `json:"hasPreviousPage"`
		StartCursor     *string `json:"startCursor"`
		EndCursor       *string `json:"endCursor"`
	} `json:"pageInfo"`
}

// cleanRecord drops fields that the REST batch endpoint refuses on write:
//   - RELATION fields (nested object form, e.g. "company": {...}); the flat
//     FK ("companyId") is kept since the batch POST accepts that shape.
//   - TS_VECTOR fields (e.g. "searchVector"); server computes these, write is
//     rejected with "TS_VECTOR-typed field does not support write operations".
func cleanRecord(raw RecordPayload, fields []FieldMetadata) RecordPayload {
	drop := make(map[string]struct{})
	for _, f := range fields {
		if f.Type == "RELATION" || f.Type == "TS_VECTOR" {
			drop[f.Name] = struct{}{}
		}
	}
	out := make(RecordPayload, len(raw))
	for k, v := range raw {
		if _, skip := drop[k]; !skip {
			out[k] = v
		}
	}
	return out
}

func fetchAllRecords(client *TwentyClient, obj ObjectMetadata) ([]RecordPayload, error) {
	all := make([]RecordPayload, 0)
	var cursor string

	for {
		params := url.Values{}
		params.Set("limit", "60")
		params.Set("order_by", "id[AscNullsLast]")
		if cursor != "" {
			params.Set("starting_after", cursor)
		}

		var res restListResponse
		if err := client.Get("/rest/"+obj.NamePlural+"?"+params.Encode(), &res); err != nil {
			return nil, err
		}

		items := res.Data[obj.NamePlural]
		for _, raw := range items {
			all = append(all, cleanRecord(raw, obj.Fields))
		}

		fmt.Fprintf(os.Stdout, "\r  %s: %d/%d   ", obj.NamePlural, len(all), res.TotalCount)

		if !res.PageInfo.HasNextPage || res.PageInfo.EndCursor == nil {
			break
		}
		cursor = *res.PageInfo.EndCursor
	}

	return all, nil
}

// twentyStandardApplicationUID is the fixed universalIdentifier of Twenty's
// built-in "standard" application. Objects/fields whose applicationId differs
// from that application's id are user-created (custom). Since Twenty 1.28+
// dropped the isCustom flag from the metadata API, this is how we derive it.
const twentyStandardApplicationUID = "20202020-64aa-4b6f-b003-9c74b97cee20"

const fetchApplicationsQuery = `
  query FindStandardApplication {
    findManyApplications {
      id
      universalIdentifier
    }
  }
`

type applicationsResponse struct {
	FindManyApplications []struct {
		ID                  string `json:"id"`
		UniversalIdentifier string `json:"universalIdentifier"`
	} `json:"findManyApplications"`
}

// resolveStandardAppID returns the id of the standard application on the source
// instance. It asks findManyApplications first; if that query is unavailable or
// the API key lacks the APPLICATIONS permission, it falls back to the most
// common applicationId across objects (standard objects always dominate).
func resolveStandardAppID(client *TwentyClient, schema schemaResponse) string {
	var apps applicationsResponse
	if err := client.MetaGraphQL(fetchApplicationsQuery, nil, &apps); err == nil {
		for _, a := range apps.FindManyApplications {
			if a.UniversalIdentifier == twentyStandardApplicationUID {
				return a.ID
			}
		}
	}

	counts := map[string]int{}
	best, bestN := "", 0
	for _, edge := range schema.Objects.Edges {
		id := edge.Node.ApplicationID
		counts[id]++
		if counts[id] > bestN {
			best, bestN = id, counts[id]
		}
	}
	fmt.Fprintln(os.Stderr, "  note: applications query unavailable; inferring standard app from most common applicationId")
	return best
}

func RunBackup(client *TwentyClient, outputPath string) error {
	fmt.Println("Fetching schema…")
	var schema schemaResponse
	if err := client.MetaGraphQL(fetchSchemaQuery, nil, &schema); err != nil {
		return err
	}

	stdAppID := resolveStandardAppID(client, schema)

	objects := make([]ObjectMetadata, 0, len(schema.Objects.Edges))
	for _, edge := range schema.Objects.Edges {
		n := edge.Node
		fields := make([]FieldMetadata, 0, len(n.Fields.Edges))
		for _, fe := range n.Fields.Edges {
			f := fe.Node.FieldMetadata
			f.IsCustom = fe.Node.ApplicationID != stdAppID
			fields = append(fields, f)
		}
		objects = append(objects, ObjectMetadata{
			ID:            n.ID,
			NameSingular:  n.NameSingular,
			NamePlural:    n.NamePlural,
			LabelSingular: n.LabelSingular,
			LabelPlural:   n.LabelPlural,
			Description:   n.Description,
			Icon:          n.Icon,
			IsCustom:      n.ApplicationID != stdAppID,
			IsActive:      n.IsActive,
			IsSystem:      n.IsSystem,
			Fields:        fields,
		})
	}
	fmt.Printf("Schema: %d objects\n", len(objects))

	records := make(map[string][]RecordPayload)

	for _, obj := range objects {
		if !includeObjectRecords(obj) {
			continue
		}
		rows, err := fetchAllRecords(client, obj)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "\r  %s: %d records\n", obj.NamePlural, len(rows))
		records[obj.NamePlural] = rows
	}

	backup := BackupFile{
		Version:   BackupFormatVersion,
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Objects:   objects,
		Records:   records,
	}

	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nBackup written → %s\n", outputPath)

	for name, rows := range records {
		if len(rows) > 0 {
			fmt.Printf("  %s: %d\n", name, len(rows))
		}
	}
	return nil
}
