// regctl — companion CLI for the Foundry registry.
//
// Usage:
//
//	regctl repos
//	regctl tags <repo>
//	regctl inspect <repo>:<tag> | <repo>@<digest>
//	regctl delete  <repo>:<tag> | <repo>@<digest>
//	regctl gc [-dry-run]
//
// Global flags (e.g. -registry) must precede the command.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	registryURL = envOr("FOUNDRY_URL", "http://localhost:8080")
	client      = &http.Client{Timeout: 30 * time.Second}
)

func main() {
	flag.StringVar(&registryURL, "registry", registryURL, "registry base URL")
	flag.Usage = usage
	flag.Parse()
	registryURL = strings.TrimRight(registryURL, "/")

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "repos":
		err = cmdRepos()
	case "tags":
		if len(args) < 2 {
			fatalf("usage: regctl tags <repo>\n")
		}
		err = cmdTags(args[1])
	case "inspect":
		if len(args) < 2 {
			fatalf("usage: regctl inspect <repo>:<tag> | <repo>@<digest>\n")
		}
		err = cmdInspect(args[1])
	case "delete":
		if len(args) < 2 {
			fatalf("usage: regctl delete <repo>:<tag> | <repo>@<digest>\n")
		}
		err = cmdDelete(args[1])
	case "gc":
		err = cmdGC(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdRepos() error {
	var repos []struct {
		ID        int64     `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := apiGet("/api/repos", &repos); err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}
	fmt.Printf("%-40s  %s\n", "REPOSITORY", "CREATED")
	fmt.Println(strings.Repeat("-", 60))
	for _, r := range repos {
		fmt.Printf("%-40s  %s\n", r.Name, r.CreatedAt.Format("2006-01-02"))
	}
	return nil
}

func cmdTags(repo string) error {
	var tags []struct {
		Tag            string    `json:"tag"`
		ManifestDigest string    `json:"manifest_digest"`
		UpdatedAt      time.Time `json:"updated_at"`
	}
	if err := apiGet("/api/repos/"+repo+"/tags", &tags); err != nil {
		return err
	}
	if len(tags) == 0 {
		fmt.Println("No tags found.")
		return nil
	}
	fmt.Printf("%-30s  %-72s  %s\n", "TAG", "DIGEST", "UPDATED")
	fmt.Println(strings.Repeat("-", 115))
	for _, t := range tags {
		fmt.Printf("%-30s  %-72s  %s\n", t.Tag, t.ManifestDigest, t.UpdatedAt.Format("2006-01-02"))
	}
	return nil
}

func cmdInspect(ref string) error {
	repo, reference, _, err := splitRef(ref)
	if err != nil {
		return err
	}

	var m map[string]json.RawMessage
	if err := apiGet("/api/repos/"+repo+"/manifests/"+url.PathEscape(reference), &m); err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

func cmdDelete(ref string) error {
	repo, reference, isDigest, err := splitRef(ref)
	if err != nil {
		return err
	}
	path := "/api/tags/" + repo + "/" + url.PathEscape(reference)
	if isDigest {
		path = "/api/manifests/" + repo + "/" + url.PathEscape(reference)
	}
	if err := apiDo(http.MethodDelete, path, nil); err != nil {
		return err
	}
	fmt.Printf("Deleted %s\n", ref)
	return nil
}

func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	_ = fs.Parse(args) // ExitOnError: never returns an error

	var result struct {
		ScannedBlobs   int      `json:"scanned_blobs"`
		DeletedBlobs   int      `json:"deleted_blobs"`
		FreedBytes     int64    `json:"freed_bytes"`
		DeletedUploads int      `json:"deleted_uploads"`
		DryRun         bool     `json:"dry_run"`
		Errors         []string `json:"errors"`
	}
	path := "/api/gc"
	if *dryRun {
		path += "?dry_run=true"
	}
	if err := apiDo(http.MethodPost, path, &result); err != nil {
		return err
	}
	if result.DryRun {
		fmt.Printf("GC dry run (nothing deleted)\n")
	} else {
		fmt.Printf("GC complete\n")
	}
	fmt.Printf("  Scanned blobs:    %d\n", result.ScannedBlobs)
	fmt.Printf("  Deleted blobs:    %d\n", result.DeletedBlobs)
	fmt.Printf("  Freed:            %s\n", humanBytes(result.FreedBytes))
	fmt.Printf("  Expired uploads:  %d\n", result.DeletedUploads)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:\n")
		for _, e := range result.Errors {
			fmt.Printf("    - %s\n", e)
		}
		return fmt.Errorf("gc finished with %d error(s)", len(result.Errors))
	}
	return nil
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func apiGet(path string, out any) error {
	return apiDo(http.MethodGet, path, out)
}

// apiDo performs a request against the admin API and decodes a JSON response
// into out (if non-nil). Non-2xx responses are returned as errors.
func apiDo(method, path string, out any) error {
	req, err := http.NewRequest(method, registryURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// splitRef parses "repo:tag" or "repo@sha256:...". The repository part may
// contain a registry host with a port ("localhost:8080/app:v1"), so the tag
// separator is the last ':' after the last '/'.
func splitRef(ref string) (repo, reference string, isDigest bool, err error) {
	if r, d, ok := strings.Cut(ref, "@"); ok {
		if r == "" || d == "" {
			return "", "", false, fmt.Errorf("invalid reference %q — expected repo@digest", ref)
		}
		return r, d, true, nil
	}
	slash := strings.LastIndex(ref, "/")
	idx := strings.LastIndex(ref, ":")
	if idx <= slash || idx == len(ref)-1 || idx == 0 {
		return "", "", false, fmt.Errorf("invalid reference %q — expected repo:tag or repo@digest", ref)
	}
	return ref[:idx], ref[idx+1:], false, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func usage() {
	fmt.Fprintln(os.Stderr, `regctl — Foundry Registry CLI

Usage: regctl [-registry <url>] <command> [args]

Commands:
  repos                    List all repositories
  tags <repo>              List tags in a repository
  inspect <repo>:<tag>     Show manifest JSON (also accepts <repo>@<digest>)
  delete <repo>:<tag>      Delete a tag
  delete <repo>@<digest>   Delete a manifest and every tag pointing at it
  gc [-dry-run]            Run garbage collection

Environment:
  FOUNDRY_URL   Registry base URL (default: http://localhost:8080)

Flags (must precede the command):
  -registry <url>   Override registry URL`)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
