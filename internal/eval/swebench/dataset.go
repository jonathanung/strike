package swebench

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// LoadInstancesJSONL reads Instance records from a JSONL (or JSON array) file.
func LoadInstancesJSONL(path string) ([]Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseInstances(data)
}

// ParseInstances accepts JSONL (one object per line) or a JSON array.
func ParseInstances(data []byte) ([]Instance, error) {
	trim := bytesTrimSpace(data)
	if len(trim) == 0 {
		return nil, fmt.Errorf("swebench: empty dataset")
	}
	if trim[0] == '[' {
		var all []Instance
		if err := json.Unmarshal(trim, &all); err != nil {
			return nil, fmt.Errorf("swebench: dataset json array: %w", err)
		}
		return validateInstances(all)
	}
	var all []Instance
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	// Problem statements can be large.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var in Instance
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			return nil, fmt.Errorf("swebench: dataset line %d: %w", lineNo, err)
		}
		all = append(all, in)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("swebench: dataset scan: %w", err)
	}
	return validateInstances(all)
}

func validateInstances(all []Instance) ([]Instance, error) {
	if len(all) == 0 {
		return nil, fmt.Errorf("swebench: dataset has no instances")
	}
	for i, in := range all {
		if in.InstanceID == "" {
			return nil, fmt.Errorf("swebench: dataset record %d: missing instance_id", i)
		}
		if in.ProblemStatement == "" {
			return nil, fmt.Errorf("swebench: %s: missing problem_statement", in.InstanceID)
		}
	}
	return all, nil
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// DatasetClient loads instances from the HuggingFace datasets-server API.
type DatasetClient struct {
	HTTP    *http.Client
	BaseURL string // default https://datasets-server.huggingface.co
	Dataset string // default DatasetName
}

func (c *DatasetClient) http() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 120 * time.Second}
}

func (c *DatasetClient) base() string {
	if c != nil && c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://datasets-server.huggingface.co"
}

func (c *DatasetClient) dataset() string {
	if c != nil && c.Dataset != "" {
		return c.Dataset
	}
	return DatasetName
}

// FetchInstances downloads all rows for the configured dataset (paginated).
func (c *DatasetClient) FetchInstances(ctx context.Context) ([]Instance, error) {
	const page = 100
	var all []Instance
	offset := 0
	for {
		batch, total, err := c.fetchPage(ctx, offset, page)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		offset += len(batch)
		if len(batch) == 0 || offset >= total {
			break
		}
	}
	return validateInstances(all)
}

// FetchSubset downloads the full dataset and filters to want IDs.
// Prefer LoadInstancesJSONL when a local export is available.
func (c *DatasetClient) FetchSubset(ctx context.Context, want []string) ([]Instance, error) {
	all, err := c.FetchInstances(ctx)
	if err != nil {
		return nil, err
	}
	return FilterSubset(all, want)
}

type hfRowsResponse struct {
	Rows []struct {
		Row json.RawMessage `json:"row"`
	} `json:"rows"`
	NumRowsTotal int `json:"num_rows_total"`
}

func (c *DatasetClient) fetchPage(ctx context.Context, offset, length int) ([]Instance, int, error) {
	u, err := url.Parse(c.base() + "/rows")
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	q.Set("dataset", c.dataset())
	q.Set("config", "default")
	q.Set("split", "test")
	q.Set("offset", fmt.Sprintf("%d", offset))
	q.Set("length", fmt.Sprintf("%d", length))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.http().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("swebench: dataset fetch: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("swebench: dataset read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("swebench: dataset HTTP %s: %s", res.Status, truncate(string(body), 200))
	}
	var parsed hfRowsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, fmt.Errorf("swebench: dataset decode: %w", err)
	}
	out := make([]Instance, 0, len(parsed.Rows))
	for i, row := range parsed.Rows {
		var in Instance
		if err := json.Unmarshal(row.Row, &in); err != nil {
			return nil, 0, fmt.Errorf("swebench: dataset row %d: %w", offset+i, err)
		}
		out = append(out, in)
	}
	total := parsed.NumRowsTotal
	if total == 0 {
		total = offset + len(out)
	}
	return out, total, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
