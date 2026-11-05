// regctl — companion CLI for the Foundry registry.
//
// Usage:
//
//	regctl repos
//	regctl tags <repo>
//	regctl inspect <repo>:<tag>
//	regctl delete <repo>:<tag>
//	regctl gc
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	flag.Parse()

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
			fatalf("usage: regctl inspect <repo>:<tag>\n")
		}
		err = cmdInspect(args[1])
	case "delete":
		if len(args) < 2 {
			fatalf("usage: regctl delete <repo>:<tag>\n")
		}
		err = cmdDelete(args[1])
	case "gc":
		err = cmdGC()
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
	repo, tag, err := splitRef(ref)
	if err != nil {
		return err
	}

	var m map[string]json.RawMessage
	if err := apiGet("/api/repos/"+repo+"/manifests/"+tag, &m); err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

func cmdDelete(ref string) error {
	repo, tag, err := splitRef(ref)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodDelete,
		registryURL+"/api/tags/"+repo+"/"+tag, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}
	fmt.Printf("Deleted %s:%s\n", repo, tag)
	return nil
}

func cmdGC() error {
	var result struct {
		ScannedBlobs int      `json:"ScannedBlobs"`
		DeletedBlobs int      `json:"DeletedBlobs"`
		FreedBytes   int64    `json:"FreedBytes"`
		Errors       []string `json:"Errors"`
	}
	req, err := http.NewRequest(http.MethodPost, registryURL+"/api/gc", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	fmt.Printf("GC complete\n")
	fmt.Printf("  Scanned blobs:  %d\n", result.ScannedBlobs)
	fmt.Printf("  Deleted blobs:  %d\n", result.DeletedBlobs)
	fmt.Printf("  Freed bytes:    %d\n", result.FreedBytes)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:\n")
		for _, e := range result.Errors {
			fmt.Printf("    - %s\n", e)
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func apiGet(path string, out interface{}) error {
	resp, err := client.Get(registryURL + path)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

func splitRef(ref string) (repo, tag string, err error) {
	idx := strings.LastIndex(ref, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("invalid reference %q — expected repo:tag", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `regctl — Foundry Registry CLI

Commands:
  repos                  List all repositories
  tags <repo>            List tags in a repository
  inspect <repo>:<tag>   Show manifest JSON
  delete <repo>:<tag>    Delete a tag
  gc                     Run garbage collection

Environment:
  FOUNDRY_URL   Registry base URL (default: http://localhost:8080)

Flags:
  -registry <url>   Override registry URL`)
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
