// tos-tag-admin is a JSON-first client for the local management API.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	baseURL := flag.String("url", "http://127.0.0.1:8090", "tos-tag management URL")
	token := flag.String("token", os.Getenv("TOS_TAG_ADMIN_TOKEN"), "management bearer token")
	flag.Parse()
	command := "status"
	if flag.NArg() > 0 {
		command = flag.Arg(0)
	}
	paths := map[string]string{"status": "/admin/api/status", "jobs": "/admin/api/jobs", "deliveries": "/admin/api/deliveries", "decisions": "/admin/api/decisions", "routes": "/admin/api/routes", "retention": "/admin/api/retention"}
	path, ok := paths[command]
	if ok {
		do(*baseURL, *token, http.MethodGet, path, "", nil, true)
		return
	}
	if command == "inject" && flag.NArg() == 2 {
		payload, err := os.ReadFile(flag.Arg(1))
		if err != nil {
			fail(err)
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			fail(fmt.Errorf("invalid fixture JSON: %w", err))
		}
		csrf := fetchCSRF(*baseURL, *token)
		do(*baseURL, *token, http.MethodPost, "/admin/api/stub/envelopes", csrf, payload, true)
		return
	}
	if command == "route-preview" && flag.NArg() == 2 {
		payload, err := os.ReadFile(flag.Arg(1))
		if err != nil {
			fail(err)
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			fail(fmt.Errorf("invalid preview JSON: %w", err))
		}
		do(*baseURL, *token, http.MethodPost, "/admin/api/routes/preview", fetchCSRF(*baseURL, *token), payload, true)
		return
	}
	fail(fmt.Errorf("usage: tos-tag-admin [flags] status|jobs|deliveries|decisions|routes|retention|route-preview FILE|inject FILE"))
}

func fetchCSRF(baseURL, token string) string {
	body := do(baseURL, token, http.MethodGet, "/admin/api/csrf", "", nil, false)
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		fail(fmt.Errorf("management API returned an invalid CSRF token"))
	}
	return result.Token
}

func do(baseURL, token, method, path, csrf string, body []byte, emit bool) []byte {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		fail(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-TOS-TAG-CSRF", csrf)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		fail(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		fail(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fail(fmt.Errorf("management API %s: %s", response.Status, strings.TrimSpace(string(data))))
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		data = append(pretty.Bytes(), '\n')
	}
	if emit {
		_, _ = os.Stdout.Write(data)
	}
	return data
}

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "tos-tag-admin: %v\n", err)
	os.Exit(1)
}
