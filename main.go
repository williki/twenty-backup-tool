package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

const version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `twenty-backup %s — backup and restore TwentyCRM data via API

Usage:
  twenty-backup backup  --url URL --token KEY [--output PATH]
  twenty-backup restore --url URL --token KEY --input PATH [--dry-run]

Flags:
  -u, --url       TwentyCRM base URL (e.g. https://twenty.example.com)
  -t, --token     API key (Settings → API & Webhooks)
  -o, --output    Backup output file (default: twenty-backup-YYYY-MM-DD.json)
  -i, --input     Backup file to restore
      --dry-run   Preview without making changes
`, version)
}

// addAliased registers both --long and -short forms pointing at the same var.
func addStringVar(fs *flag.FlagSet, p *string, def, usage string, names ...string) {
	for _, n := range names {
		fs.StringVar(p, n, def, usage)
	}
}

func addBoolVar(fs *flag.FlagSet, p *bool, usage string, names ...string) {
	for _, n := range names {
		fs.BoolVar(p, n, false, usage)
	}
}

func requireCredentials(url, token string) {
	if url == "" || token == "" {
		fmt.Fprintln(os.Stderr, "error: --url and --token are required")
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "backup":
		runBackupCmd(os.Args[2:])
	case "restore":
		runRestoreCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	case "-v", "--version", "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func runBackupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	fs.Usage = usage

	today := time.Now().Format("2006-01-02")
	var url, token, output string

	addStringVar(fs, &url, "", "TwentyCRM base URL", "url", "u")
	addStringVar(fs, &token, "", "API key", "token", "t")
	addStringVar(fs, &output, "twenty-backup-"+today+".json", "Output file", "output", "o")

	_ = fs.Parse(args)

	requireCredentials(url, token)

	client := NewTwentyClient(url, token)
	if err := RunBackup(client, output); err != nil {
		fmt.Fprintln(os.Stderr, "Backup failed:", err)
		os.Exit(1)
	}
}

func runRestoreCmd(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	fs.Usage = usage

	var url, token, input string
	var dryRun bool

	addStringVar(fs, &url, "", "TwentyCRM base URL", "url", "u")
	addStringVar(fs, &token, "", "API key", "token", "t")
	addStringVar(fs, &input, "", "Backup file to restore", "input", "i")
	addBoolVar(fs, &dryRun, "Preview without making changes", "dry-run")

	_ = fs.Parse(args)

	if input == "" {
		fmt.Fprintln(os.Stderr, "error: --input is required")
		os.Exit(1)
	}

	requireCredentials(url, token)

	client := NewTwentyClient(url, token)
	if err := RunRestore(client, input, dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "Restore failed:", err)
		os.Exit(1)
	}
}
