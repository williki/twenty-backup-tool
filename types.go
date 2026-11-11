package main

import "encoding/json"

const BackupFormatVersion = "1"

type BackupFile struct {
	Version   string                     `json:"version"`
	CreatedAt string                     `json:"createdAt"`
	Objects   []ObjectMetadata           `json:"objects"`
	Records   map[string][]RecordPayload `json:"records"`
}

// RecordPayload is an arbitrary JSON object. We keep it as a decoded map so
// that cleanRecord can drop specific keys during backup; on restore it is
// re-serialized as-is.
type RecordPayload map[string]any

type ObjectMetadata struct {
	ID            string          `json:"id"`
	NameSingular  string          `json:"nameSingular"`
	NamePlural    string          `json:"namePlural"`
	LabelSingular string          `json:"labelSingular"`
	LabelPlural   string          `json:"labelPlural"`
	Description   *string         `json:"description"`
	Icon          *string         `json:"icon"`
	IsCustom      bool            `json:"isCustom"`
	IsActive      bool            `json:"isActive"`
	IsSystem      bool            `json:"isSystem"`
	Fields        []FieldMetadata `json:"fields"`
}

type FieldMetadata struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	Type         string          `json:"type"`
	Description  *string         `json:"description"`
	Icon         *string         `json:"icon"`
	IsCustom     bool            `json:"isCustom"`
	IsNullable   bool            `json:"isNullable"`
	IsSystem     bool            `json:"isSystem"`
	IsActive     bool            `json:"isActive"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	Settings     json.RawMessage `json:"settings,omitempty"`
	Relation     *Relation       `json:"relation,omitempty"`
}

type Relation struct {
	Type                 string        `json:"type"` // MANY_TO_ONE | ONE_TO_MANY
	SourceObjectMetadata RelationRef   `json:"sourceObjectMetadata"`
	TargetObjectMetadata RelationRef   `json:"targetObjectMetadata"`
	SourceFieldMetadata  RelationField `json:"sourceFieldMetadata"`
	TargetFieldMetadata  RelationField `json:"targetFieldMetadata"`
}

type RelationRef struct {
	ID           string `json:"id"`
	NameSingular string `json:"nameSingular"`
}

type RelationField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
