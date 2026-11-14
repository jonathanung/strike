package deploywatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"
)

const probeTimeout = 10 * time.Second

// probeHTTP GETs url. A transport error is unreachable; a status other than
// expect is failing. The body is drained (bounded) and ignored. Details never
// echo the URL, which may carry a credential in its query.
func probeHTTP(ctx context.Context, client *http.Client, url string, expect int) (state, detail string) {
	if expect == 0 {
		expect = http.StatusOK
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StateUnreachable, "invalid probe URL"
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return StateUnreachable, clip(err.Error(), maxDetail)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != expect {
		return StateFailing, fmt.Sprintf("status %d, want %d", resp.StatusCode, expect)
	}
	return StateHealthy, fmt.Sprintf("status %d", resp.StatusCode)
}
