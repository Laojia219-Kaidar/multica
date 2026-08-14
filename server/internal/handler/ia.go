package handler

import (
	"net/http"
)

// ObjectOwnershipEntry is one L1 capability domain and the objects it owns.
// Canonical source: WO-IA-02 (L1/L2/L3 IA + 对象归属). Runtime-verifiable projection.
type ObjectOwnershipEntry struct {
	Domain         string   `json:"domain"`
	DomainKey      string   `json:"domain_key"`
	Objects        []string `json:"objects"`
	CanonicalWriter string  `json:"canonical_writer"`
}

// GetObjectOwnership returns the 11-capability-domain object-ownership matrix.
// This is a read-only governance read model; the authoritative map lives in
// WO-IA-02 and is re-derived here so the IA is runtime-verifiable, not prose-only.
func (h *Handler) GetObjectOwnership(w http.ResponseWriter, r *http.Request) {
	matrix := []ObjectOwnershipEntry{
		{Domain: "CEO总指挥台", DomainKey: "ceo", Objects: []string{"Project 组合"}, CanonicalWriter: "William/CEO"},
		{Domain: "工作经营", DomainKey: "work", Objects: []string{"Project", "WorkOrder", "Task", "Run"}, CanonicalWriter: "HiveCrew 原生"},
		{Domain: "成果中心", DomainKey: "outcomes", Objects: []string{"Outcome", "Artifact"}, CanonicalWriter: "另一 Prime 窗口"},
		{Domain: "组织与人才", DomainKey: "org", Objects: []string{"Department", "Position", "Appointment", "Employee"}, CanonicalWriter: "本增补 B"},
		{Domain: "数字员工工厂", DomainKey: "employeeFactory", Objects: []string{"Employee 生产/上岗/能力包"}, CanonicalWriter: "本增补 B"},
		{Domain: "协作空间", DomainKey: "workroom", Objects: []string{"QM Workroom"}, CanonicalWriter: "本增补 D"},
		{Domain: "数据与知识", DomainKey: "dataKnowledge", Objects: []string{"Dataset", "KB", "KG", "EvalSet"}, CanonicalWriter: "本增补 C"},
		{Domain: "模型与智能", DomainKey: "modelIntelligence", Objects: []string{"Model", "Runtime 目录", "用量"}, CanonicalWriter: "HiveCrew 原生"},
		{Domain: "基地与基础设施", DomainKey: "baseInfra", Objects: []string{"Base", "Runtime", "基础设施"}, CanonicalWriter: "HiveCrew 原生 + 基地迁移"},
		{Domain: "治理与安全", DomainKey: "governance", Objects: []string{"Policy", "凭据", "配置"}, CanonicalWriter: "HiveCosm 治理"},
		{Domain: "设置", DomainKey: "settings", Objects: []string{"配置"}, CanonicalWriter: "HiveCrew"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": matrix, "count": len(matrix)})
}
