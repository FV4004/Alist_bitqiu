package bitqiu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Page requests are serialized by clientMu. Pace start times across directories
// as well as pages; authentication's single refresh also uses this gate.
func (d *BitQiu) pageInterval() time.Duration {
	ms, err := strconv.Atoi(strings.TrimSpace(d.PageIntervalMS))
	if err != nil || ms < 0 {
		return time.Second
	}
	if ms > 30000 {
		return 30 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}
func (d *BitQiu) waitClientPage(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	if d.pageDelayWait != nil {
		return d.pageDelayWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (d *BitQiu) paceClientPage(ctx context.Context) error {
	delay := time.Duration(0)
	if !d.lastPageRequest.IsZero() {
		delay = d.pageInterval() - time.Since(d.lastPageRequest)
	}
	if err := d.waitClientPage(ctx, delay); err != nil {
		return err
	}
	d.lastPageRequest = time.Now()
	return nil
}
func clientRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 30 {
			return 31 * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until.Sub(now)
	}
	return 0
}

// Only retry this read-only directory operation, and only an empty 423 with no
// business code. A 423 does not establish token expiry or permission to log in.
// Never advance the cursor until the page was successfully returned and parsed.
func (d *BitQiu) clientPageRequest(ctx context.Context, form url.Values) (clientResponse, error) {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := d.authenticatedClientRequest(ctx, diagnosticEndpoint, form)
		if err == nil {
			return resp, nil
		}
		var locked *clientHTTPError
		if !errors.As(err, &locked) || locked.Status != 423 || locked.BodyBytes != 0 || locked.Code != "" {
			return resp, err
		}
		if attempt == 2 {
			return resp, fmt.Errorf("BitQiu 当前页退避重试两次后仍失败：%w", err)
		}
		delay := time.Duration(2<<attempt) * time.Second
		if locked.RetryAfter > 30*time.Second {
			return resp, fmt.Errorf("BitQiu 服务端要求等待超过 30 秒，请稍后重试：%w", err)
		}
		if locked.RetryAfter > delay {
			delay = locked.RetryAfter
		}
		if waitErr := d.waitClientPage(ctx, delay); waitErr != nil {
			return resp, waitErr
		}
	}
	return clientResponse{}, errors.New("BitQiu 目录重试已结束")
}
