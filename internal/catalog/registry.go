package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type registryClient struct {
	baseURL string
	http    *http.Client
}

type providerVersionsResponse struct {
	Versions []struct {
		Version string `json:"version"`
	} `json:"versions"`
}

type moduleListResponse struct {
	Meta struct {
		NextOffset *int `json:"next_offset"`
	} `json:"meta"`
	Modules []json.RawMessage `json:"modules"`
}

func newRegistryClient(baseURL string, timeout time.Duration) *registryClient {
	if baseURL == "" {
		baseURL = "https://registry.terraform.io"
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &registryClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *registryClient) latestProviderVersion(source string) (string, error) {
	parts := strings.Split(strings.Trim(source, "/"), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("provider source %q must be namespace/name", source)
	}
	endpoint := fmt.Sprintf("%s/v1/providers/%s/%s/versions", c.baseURL, url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	var response providerVersionsResponse
	if err := c.getJSON(endpoint, &response); err != nil {
		return "", err
	}
	var stable []string
	for _, version := range response.Versions {
		if version.Version != "" && !strings.Contains(version.Version, "-") {
			stable = append(stable, version.Version)
		}
	}
	if len(stable) == 0 {
		return "", fmt.Errorf("registry returned no stable versions for %s", source)
	}
	sort.Slice(stable, func(i, j int) bool { return compareSemver(stable[i], stable[j]) > 0 })
	return stable[0], nil
}

func (c *registryClient) modules(provider string, limit int, includeDetails bool, workers, requestsPerSecond int, refreshedAt time.Time, progress func(string, ...interface{})) (*ModuleCatalog, error) {
	catalog := &ModuleCatalog{FormatVersion: FormatVersion, RefreshedAt: refreshedAt, Provider: provider}
	offset := 0
	pageSize := 100
	for {
		remaining := limit - len(catalog.Modules)
		if limit > 0 && remaining < pageSize {
			pageSize = remaining
		}
		if pageSize <= 0 {
			break
		}
		query := url.Values{}
		query.Set("provider", provider)
		query.Set("offset", strconv.Itoa(offset))
		query.Set("limit", strconv.Itoa(pageSize))
		endpoint := c.baseURL + "/v1/modules?" + query.Encode()
		var response moduleListResponse
		if err := c.getJSON(endpoint, &response); err != nil {
			return nil, fmt.Errorf("list %s modules: %w", provider, err)
		}
		for _, raw := range response.Modules {
			var record ModuleRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, fmt.Errorf("decode %s module record: %w", provider, err)
			}
			record.Raw = raw
			catalog.Modules = append(catalog.Modules, record)
			if limit > 0 && len(catalog.Modules) >= limit {
				break
			}
		}
		if progress != nil {
			progress("registry modules: %s %d", provider, len(catalog.Modules))
		}
		if (limit > 0 && len(catalog.Modules) >= limit) || response.Meta.NextOffset == nil || len(response.Modules) == 0 {
			break
		}
		offset = *response.Meta.NextOffset
	}

	sort.Slice(catalog.Modules, func(i, j int) bool { return catalog.Modules[i].ID < catalog.Modules[j].ID })
	if includeDetails && len(catalog.Modules) > 0 {
		details, err := c.moduleDetails(catalog.Modules, workers, requestsPerSecond, progress)
		if err != nil {
			return nil, err
		}
		catalog.Details = details
	}
	return catalog, nil
}

func (c *registryClient) moduleDetails(records []ModuleRecord, workers, requestsPerSecond int, progress func(string, ...interface{})) ([]ModuleDetail, error) {
	if workers <= 0 {
		workers = 6
	}
	if requestsPerSecond <= 0 {
		requestsPerSecond = 10
	}
	type result struct {
		detail ModuleDetail
		err    error
	}
	jobs := make(chan ModuleRecord)
	results := make(chan result)
	requestInterval := time.Second / time.Duration(requestsPerSecond)
	if requestInterval <= 0 {
		requestInterval = time.Nanosecond
	}
	requestBudget := time.NewTicker(requestInterval)
	defer requestBudget.Stop()
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for record := range jobs {
				<-requestBudget.C
				endpoint := fmt.Sprintf("%s/v1/modules/%s/%s/%s/%s",
					c.baseURL,
					url.PathEscape(record.Namespace), url.PathEscape(record.Name),
					url.PathEscape(record.Provider), url.PathEscape(record.Version))
				var raw json.RawMessage
				err := c.getJSON(endpoint, &raw)
				results <- result{detail: ModuleDetail{ID: record.ID, Raw: raw}, err: err}
			}
		}()
	}
	go func() {
		for _, record := range records {
			jobs <- record
		}
		close(jobs)
		wait.Wait()
		close(results)
	}()

	details := make([]ModuleDetail, 0, len(records))
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("get module detail %s: %w", result.detail.ID, result.err)
			}
			continue
		}
		details = append(details, result.detail)
		if progress != nil && (len(details)%25 == 0 || len(details) == len(records)) {
			progress("registry module details: %d/%d", len(details), len(records))
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(details, func(i, j int) bool { return details[i].ID < details[j].ID })
	return details, nil
}

func (c *registryClient) getJSON(endpoint string, target interface{}) error {
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		var retryAfter time.Duration
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "terra-translate-catalog/1.0")
		response, err := c.http.Do(request)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 512<<20))
			response.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else if response.StatusCode >= 200 && response.StatusCode < 300 {
				if rawTarget, ok := target.(*json.RawMessage); ok {
					*rawTarget = append((*rawTarget)[:0], body...)
					return nil
				}
				if err := json.Unmarshal(body, target); err != nil {
					return fmt.Errorf("decode %s: %w", endpoint, err)
				}
				return nil
			} else {
				lastErr = fmt.Errorf("GET %s: %s: %s", endpoint, response.Status, strings.TrimSpace(string(body)))
				if response.StatusCode == http.StatusTooManyRequests {
					retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
					if retryAfter == 0 {
						retryAfter = time.Minute
					}
				} else if response.StatusCode < 500 {
					return lastErr
				}
			}
		}
		if attempt < 7 {
			delay := time.Duration(1<<attempt) * time.Second
			if retryAfter > delay {
				delay = retryAfter
			}
			time.Sleep(delay)
		}
	}
	return lastErr
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func compareSemver(left, right string) int {
	l := parseVersion(left)
	r := parseVersion(right)
	for i := 0; i < 3; i++ {
		if l[i] < r[i] {
			return -1
		}
		if l[i] > r[i] {
			return 1
		}
	}
	return strings.Compare(left, right)
}

func parseVersion(version string) [3]int {
	version = strings.TrimPrefix(version, "v")
	version = strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(version, ".")
	var parsed [3]int
	for i := 0; i < len(parts) && i < len(parsed); i++ {
		parsed[i], _ = strconv.Atoi(parts[i])
	}
	return parsed
}
