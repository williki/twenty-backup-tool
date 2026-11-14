# twenty-backup-tool

Backup and restore [TwentyCRM](https://twenty.com) data via the API. Exports your full schema (custom objects, custom fields) and all records to a JSON file, and can replay that backup into a fresh instance.

## Why not just back up the database?

A full Postgres dump of the Twenty database is still a good idea, but it does not help when a Twenty update fails mid-migration. The dump is tied to the old schema version, so restoring it only takes you back to the broken starting point. This tool exports your data at the API level, independent of the internal schema, so you can restore it into a fresh, working instance of any version.

The built-in alternative is Twenty's CSV export. It works object by object, has to be done manually, and captures far less: no schema definitions, no custom field settings, and no preserved record IDs, so relations between records are lost. This tool backs up the full schema and all records in one run.

## What gets backed up

| Data                    | Backed up | Notes                                        |
| ----------------------- | --------- | -------------------------------------------- |
| Custom objects          | yes       | Full schema definition                       |
| Custom fields           | yes       | All types incl. select options and settings  |
| Relations               | yes       | Captured from the owning (FROM) side         |
| All records             | yes       | People, Companies, custom objects, etc.      |
| Original record IDs     | yes       | UUIDs are preserved so relations stay intact |
| Views / filters / sorts | **no**    | No API available                             |
| Workflows / automations | **no**    | No API available                             |
| Roles & permissions     | **no**    | No management API available                  |
| File attachments        | **no**    | No download/upload API available             |

## Requirements

- Go 1.22+
- A TwentyCRM API key — generate one at **Settings → API & Webhooks → Create key**

## Install

```bash
go build             # produces ./twenty-backup-tool
```

Or run directly without building:

```bash
go run . backup ...
```

## Usage

Both `--url` and `--token` are required.

### Backup

```bash
./twenty-backup-tool backup \
  --url   https://api.twenty.com \
  --token YOUR_API_KEY \
  --output twenty-backup-2026-08-20.json   # optional, defaults to today's date
```

### Restore

```bash
./twenty-backup-tool restore \
  --url   https://new-instance.twenty.com \
  --token NEW_API_KEY \
  --input twenty-backup-2026-08-20.json
```

Preview what would happen without making any changes:

```bash
./twenty-backup-tool restore ... --dry-run
```

## Backup file format

A single JSON file with two top-level keys:

```jsonc
{
  "version": "1",
  "createdAt": "2026-08-20T12:00:00.000Z",
  "objects": [ /* full schema for every object */ ],
  "records": {
    "people":    [ /* all person records */ ],
    "companies": [ /* all company records */ ],
    /* one key per active non-system object */
  }
}
```

Records are stored in the same shape returned by the REST API, including composite fields (`name`, `emails`, `links`, etc.) as nested objects, but with the nested relation objects stripped so only flat FKs (`companyId`, etc.) remain.

## Restore behaviour

1. **Custom objects** — created via the metadata GraphQL API; skipped if they already exist.
2. **Custom fields** — created on both standard and custom objects; skipped if already present.
3. **Records** — batch-inserted (60 per request) via the REST batch API. Records are inserted in dependency order so that related objects are available before the records that reference them. Original UUIDs are preserved, keeping all cross-object relations intact.

Running restore a second time is safe: object/field creation is idempotent (skips duplicates), and record creation will fail on duplicate IDs (add `?upsert=true` to the `/rest/batch/{namePlural}` URL in `restore.go` if you need idempotent re-runs).

## Rate limiting

The API allows 100 requests per minute. The client stays at 90 req/min by default and backs off automatically on `429` responses. Large workspaces will take proportionally longer — roughly 6 000 records per minute at maximum throughput.

## Upgrading TwentyCRM

The intended workflow for a version upgrade:

```
1. twenty-backup-tool backup  --url OLD --token OLD_KEY  -o backup.json
2. Deploy new Twenty version (fresh install or in-place upgrade)
3. twenty-backup-tool restore --url NEW --token NEW_KEY  -i backup.json
```

After restore, recreate views, workflows, and permissions manually (see table above).

## Development

```bash
go build
go vet ./...
./twenty-backup-tool --help
```

Source layout:

```
main.go       CLI entry point (subcommand dispatch via stdlib flag)
client.go     HTTP client with rate limiting and 429 retry
backup.go     Schema + record export logic
restore.go    Schema + record import logic
types.go      Shared types (BackupFile, ObjectMetadata, FieldMetadata, Relation)
```
