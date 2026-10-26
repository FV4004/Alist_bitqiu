package handles

import (
	"encoding/csv"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

func inventoryExportGroup(v nasCompareItem) string {
	decision := inventoryDecision(v.Detail)
	if decision == "missing" {
		return "confirmed_missing"
	}
	if v.Category == "suspected_missing" && decision == "pending" {
		return "suspected_missing"
	}
	if decision == "pending" || v.Detail != nil && v.Detail.Stale {
		return "pending"
	}
	return "other"
}
func inventoryCSVCell(s string) string {
	// Protect spreadsheet formula execution, including whitespace-prefixed cells.
	t := strings.TrimLeft(s, " \t\r\n")
	if len(t) > 0 && strings.ContainsRune("=+-@", rune(t[0])) {
		return "'" + s
	}
	return s
}
func BitQiuNASInventoryExport(c *gin.Context) {
	var req struct {
		ScanID string   `json:"scan_id"`
		Format string   `json:"format"`
		Groups []string `json:"groups"`
	}
	if c.ShouldBindJSON(&req) != nil || (req.Format != "csv" && req.Format != "json") {
		common.ErrorStrResp(c, "请选择 CSV 或 JSON", 400)
		return
	}
	groups := map[string]bool{}
	for _, g := range req.Groups {
		switch g {
		case "suspected_missing", "confirmed_missing", "pending", "other", "issues":
			groups[g] = true
		default:
			common.ErrorStrResp(c, "导出分类无效", 400)
			return
		}
	}
	if len(groups) == 0 {
		common.ErrorStrResp(c, "请选择导出分类", 400)
		return
	}
	user := c.MustGet("user").(*model.User)
	if !user.IsAdmin() {
		common.ErrorStrResp(c, "仅管理员可访问盘点复核", 403)
		return
	}
	nasScanStore.Lock()
	j, r, err := inventoryResultLocked(user, req.ScanID)
	if err != nil {
		for _, candidate := range nasScanStore.jobs {
			if candidate.Owner == user.ID && candidate.Options.Enabled && candidate.Partial != nil && candidate.Partial.ID == req.ScanID && common.CanReadPathByRole(user, candidate.Source) && common.CanReadPathByRole(user, candidate.Destination) {
				j, r, err = candidate, candidate.Partial, nil
				break
			}
		}
	}
	if err != nil {
		nasScanStore.Unlock()
		common.ErrorResp(c, err, 409)
		return
	}
	rows := []nasCompareItem{}
	for _, v := range r.Items {
		group := inventoryExportGroup(v.Item)
		if groups[group] || groups["pending"] && inventoryDecision(v.Item.Detail) == "pending" || groups["other"] && v.Item.Category != "suspected_missing" {
			rows = append(rows, v.Item)
		}
	}
	issues := []nasCompareIssue{}
	if groups["issues"] {
		issues = append(issues, r.Issues...)
		if j.Partial != nil {
			issues = append(issues, j.Partial.Issues...)
		}
	}
	scope := gin.H{"source": j.Source, "destination": j.Destination, "names": append([]string(nil), j.Names...), "options": j.Options, "started_at": j.StartedAt, "completed_at": r.CompletedAt, "scan_id": r.ID, "latest_job_state": j.State, "exported_at": time.Now()}
	// Copy under the journal lock; HTTP encoding must not race with review edits.
	payload, _ := json.Marshal(gin.H{"scope": scope, "items": rows, "issues": issues, "inventory": r.Inventory})
	nasScanStore.Unlock()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "attachment; filename=nas-inventory."+req.Format)
	if req.Format == "json" {
		c.Data(200, "application/json; charset=utf-8", payload)
		return
	}
	var copied struct {
		Scope  json.RawMessage   `json:"scope"`
		Items  []nasCompareItem  `json:"items"`
		Issues []nasCompareIssue `json:"issues"`
	}
	_ = json.Unmarshal(payload, &copied)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	_, _ = c.Writer.Write([]byte("\xef\xbb\xbf"))
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"scope", "relative_path", "category", "is_directory", "size", "size_known", "evidence", "review_decision", "reviewer", "review_time", "candidate", "note", "needs_reconfirmation", "scan_problem"})
	for _, v := range copied.Items {
		detail, _ := json.Marshal(v.Detail)
		decision := inventoryDecision(v.Detail)
		reviewer, at, candidate, note, stale := "", "", "", "", "false"
		known := "false"
		if v.Detail != nil {
			if v.Detail.Source != nil && v.Detail.Source.SizeKnown || v.Detail.Source == nil && v.Detail.NAS != nil && v.Detail.NAS.SizeKnown {
				known = "true"
			}
			if v.Detail.Stale {
				stale = "true"
			}
			if n := len(v.Detail.Reviews); n > 0 {
				e := v.Detail.Reviews[n-1]
				reviewer = e.ReviewerName
				at = e.At.Format(time.RFC3339)
				candidate = e.Candidate
				note = e.Note
			}
		}
		row := []string{string(copied.Scope), v.Relative, v.Category, strconv.FormatBool(v.IsDir), strconv.FormatInt(v.Size, 10), known, string(detail), decision, reviewer, at, candidate, note, stale, ""}
		for i := range row {
			row[i] = inventoryCSVCell(row[i])
		}
		_ = w.Write(row)
	}
	for _, issue := range copied.Issues {
		_ = w.Write([]string{string(copied.Scope), inventoryCSVCell(issue.Relative), "indeterminate", "", "", "", "", "", "", "", "", "", "", inventoryCSVCell(issue.Message)})
	}
	w.Flush()
}
