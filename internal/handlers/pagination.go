package handlers

import (
	"net/http"
	"strconv"
)

// PaginationParams holds computed pagination values.
type PaginationParams struct {
	Page       int
	PageSize   int
	Offset     int
	Total      int
	TotalPages int
}

// GetPaginationParams extracts the page number from the request query string
// and computes the offset for the given page size.
func (h *Handler) GetPaginationParams(r *http.Request, pageSize int) PaginationParams {
	page := 1
	if s := r.URL.Query().Get("page"); s != "" {
		if p, err := strconv.Atoi(s); err == nil && p > 0 {
			page = p
		}
	}
	return PaginationParams{
		Page:     page,
		PageSize: pageSize,
		Offset:   (page - 1) * pageSize,
	}
}

// WithTotal sets the total count and computes total pages.
func (p *PaginationParams) WithTotal(total int) {
	p.Total = total
	p.TotalPages = (total + p.PageSize - 1) / p.PageSize
}
