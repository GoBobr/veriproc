package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/eum/veriproc/internal/groups"
	"github.com/eum/veriproc/internal/httpapi/apierr"
	"github.com/eum/veriproc/internal/store"
)

type groupHandler struct {
	svc *groups.Service
}

type groupWire struct {
	SplitGroupID    string       `json:"split_group_id"`
	Label           string       `json:"label,omitempty"`
	Description     string       `json:"description,omitempty"`
	State           string       `json:"state"`
	ExpectedMembers *int         `json:"expected_members,omitempty"`
	CanonicalCount  int          `json:"canonical_count"`
	FailedCount     int          `json:"failed_count"`
	MemberCount     int          `json:"member_count"`
	Summary         string       `json:"summary,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	ClosedAt        *time.Time   `json:"closed_at,omitempty"`
	AggregatedAt    *time.Time   `json:"aggregated_at,omitempty"`
	Members         []memberWire `json:"members,omitempty"`
}

type memberWire struct {
	RunID        string    `json:"run_id"`
	TaskID       string    `json:"task_id"`
	Role         string    `json:"role,omitempty"`
	State        string    `json:"state,omitempty"`
	Canonicality string    `json:"canonicality,omitempty"`
	AddedAt      time.Time `json:"added_at"`
}

func toGroupWire(v *groups.GroupView, includeMembers bool) groupWire {
	g := v.SplitGroupRecord
	w := groupWire{
		SplitGroupID:   g.SplitGroupID,
		Label:          g.Label,
		Description:    g.Description,
		State:          g.State,
		CanonicalCount: g.CanonicalCount,
		FailedCount:    g.FailedCount,
		MemberCount:    len(v.Members),
		Summary:        g.Summary,
		CreatedAt:      g.CreatedAt.UTC(),
	}
	if g.ExpectedMembers.Valid {
		n := int(g.ExpectedMembers.Int64)
		w.ExpectedMembers = &n
	}
	if g.ClosedAt.Valid {
		t := g.ClosedAt.Time.UTC()
		w.ClosedAt = &t
	}
	if g.AggregatedAt.Valid {
		t := g.AggregatedAt.Time.UTC()
		w.AggregatedAt = &t
	}
	if includeMembers {
		w.Members = make([]memberWire, 0, len(v.Members))
		for _, m := range v.Members {
			w.Members = append(w.Members, memberWire{
				RunID:        m.RunID,
				TaskID:       m.TaskID,
				Role:         m.Role,
				State:        m.State,
				Canonicality: m.Canonicality,
				AddedAt:      m.AddedAt.UTC(),
			})
		}
	}
	return w
}

func (h *groupHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 200 {
			apierr.Write(w, r, http.StatusBadRequest, apierr.CodeInvalidRequest, "invalid limit")
			return
		}
		limit = n
	}
	gs, err := h.svc.List(r.Context(), state, limit)
	if err != nil {
		writeGroupErr(w, r, err)
		return
	}
	items := make([]groupWire, 0, len(gs))
	for _, g := range gs {
		items = append(items, toGroupWire(g, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"ordering": "created_at_desc",
		"filters":  map[string]any{"state": state},
	})
}

func (h *groupHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("group_id")
	g, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeGroupErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupWire(g, true))
}

func (h *groupHandler) close(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("group_id")
	g, err := h.svc.Close(r.Context(), id)
	if err != nil {
		writeGroupErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupWire(g, true))
}

func writeGroupErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, groups.ErrGroupNotFound):
		apierr.Write(w, r, http.StatusNotFound, apierr.CodeNotFound, "split group not found")
	case errors.Is(err, store.ErrConflict):
		apierr.Write(w, r, http.StatusConflict, apierr.CodeInvalidStateTransition, err.Error())
	default:
		apierr.Write(w, r, http.StatusInternalServerError, apierr.CodeInternal, err.Error())
	}
}
