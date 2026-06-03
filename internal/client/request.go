package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// This file holds the shared request helpers every resource client method
// reuses. Each helper performs the same canonical sequence:
//
//  1. c.do(...)                 — transport (auth, retry, body read).
//  2. responseError(resp, body) — map any non-2xx to *APIError and return.
//  3. decode*                   — unwrap the envelope; decodeItem/decodeList
//                                 also surface HTTP-200 + success:false (C3).
//
// Keeping this sequence in one place means resource methods are one-liners
// (e.g. CreateSSHKey → doItem(...,"ssh_key")) and the error-mapping order is
// identical across every resource.

// maxPaginatorPages is the safety cap for the auto-pagination loop in doList.
// It prevents an infinite loop when a server returns pathological paginator
// values (e.g. last_page == MaxInt). 10,000 pages × 12 items/page = 120,000
// items maximum — sufficient for any realistic dataset.
const maxPaginatorPages = 10_000

// doItem performs the request, maps a non-2xx response to *APIError, then
// unwraps the single object stored under key. A 200 response carrying
// success:false is also surfaced as an error (handled inside decodeItem).
//
// When key is "" the bare top-level envelope is returned (used by endpoints
// whose success payload has no nested object).
func (c *Client) doItem(ctx context.Context, method, path string, body any, key string) (map[string]any, error) {
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}
	return decodeItem(raw, key)
}

// doList performs the request, maps a non-2xx response to *APIError, then
// unwraps a list (Laravel paginator {"data":[...]} or a bare top-level array).
// A 200 response carrying success:false is surfaced as an error.
//
// When the response is a Laravel paginator (contains numeric current_page and
// last_page fields), doList automatically fetches all subsequent pages by
// appending a "page=N" query parameter to path, accumulating every page's
// items. This fixes the silent page-1-only bug where items beyond the first
// page (server-side paginate(12)) were silently discarded.
//
// The page-param is merged via net/url so paths with existing query strings
// (e.g. /things?search=x) are handled correctly: the page param is set or
// overwritten without disturbing other params.
//
// A safety cap of maxPaginatorPages prevents an infinite loop on a malformed
// server; items accumulated up to that point are returned.
//
// Top-level array responses and non-paginator objects (no current_page /
// last_page) follow the original single-fetch path unchanged.
func (c *Client) doList(ctx context.Context, method, path string, body any) ([]map[string]any, error) {
	// Fetch the first page using the path as supplied.
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if err := responseError(resp, raw); err != nil {
		return nil, err
	}

	items, err := decodeList(raw)
	if err != nil {
		return nil, err
	}

	// Inspect the raw response for Laravel paginator fields.
	currentPage, lastPage, isPaginator := paginatorPages(raw)
	if !isPaginator {
		// Top-level array or non-paginator object — single fetch, done.
		return items, nil
	}

	// Paginator: loop until we have all pages or hit the safety cap.
	for currentPage < lastPage && currentPage < maxPaginatorPages {
		nextPage := currentPage + 1
		nextPath, err := withPageParam(path, nextPage)
		if err != nil {
			return nil, fmt.Errorf("doList: building page URL for page %d: %w", nextPage, err)
		}

		resp, raw, err = c.do(ctx, method, nextPath, body)
		if err != nil {
			return nil, err
		}
		if err := responseError(resp, raw); err != nil {
			return nil, err
		}

		pageItems, err := decodeList(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, pageItems...)

		// Re-read the paginator fields from this page's response to
		// advance the loop correctly (last_page could theoretically
		// change on a live system between requests).
		cp, lp, ok := paginatorPages(raw)
		if !ok {
			// Server stopped sending paginator fields — treat as done.
			break
		}
		currentPage = cp
		lastPage = lp
	}

	return items, nil
}

// paginatorPages extracts the current_page and last_page fields from a raw
// JSON response body. JSON numbers decode as float64, so both fields are
// read as float64 and converted to int. Returns (currentPage, lastPage, true)
// when both fields are present and positive; otherwise (0, 0, false).
func paginatorPages(raw []byte) (currentPage, lastPage int, ok bool) {
	// Quick check: top-level arrays are never paginators.
	if len(raw) > 0 && raw[0] == '[' {
		return 0, 0, false
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0, 0, false
	}

	cpRaw, hasCp := top["current_page"]
	lpRaw, hasLp := top["last_page"]
	if !hasCp || !hasLp {
		return 0, 0, false
	}

	var cpF, lpF float64
	if err := json.Unmarshal(cpRaw, &cpF); err != nil {
		return 0, 0, false
	}
	if err := json.Unmarshal(lpRaw, &lpF); err != nil {
		return 0, 0, false
	}

	cp := int(cpF)
	lp := int(lpF)
	if cp <= 0 || lp <= 0 {
		return 0, 0, false
	}
	return cp, lp, true
}

// withPageParam returns path with the "page" query parameter set to page,
// preserving any existing query parameters. Uses net/url to parse and
// re-encode, so paths like /things?search=x become /things?page=2&search=x.
func withPageParam(path string, page int) (string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("page", fmt.Sprintf("%d", page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// doVoid performs the request expecting no object in the response. It maps a
// non-2xx response to *APIError and treats a 200 response carrying
// success:false as an error (C3). Used by delete-style endpoints.
func (c *Client) doVoid(ctx context.Context, method, path string, body any) error {
	resp, raw, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if err := responseError(resp, raw); err != nil {
		return err
	}
	// Reuse the success-flag logic: decodeItem with an empty key returns the
	// bare envelope and short-circuits on success:false. We discard the object.
	_, err = decodeItem(raw, "")
	return err
}
