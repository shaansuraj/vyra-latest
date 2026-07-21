package relay

import (
	"sort"
	"strings"
)

type predefinedAgentTemplate struct {
	Name        string
	Instruction string
	WorkerID    string
	Aliases     []string
}

var predefinedAgentTemplates = []predefinedAgentTemplate{
	{
		Name:        "PPE Detection",
		Instruction: "Detect PPE violations in real time. Save violation snapshots and generate a concise risk brief and AI analysis summary.",
		WorkerID:    "vyra/ppe_detection",
		Aliases:     []string{"vyra/ppe_detection", "vyra/helmet_detection"},
	},
	{
		Name:        "Person Detection",
		Instruction: "Detect and track persons with persistent IDs across cameras. Use built-in face recognition (known and unknown), trigger visit-based snapshots, and include person labels in AI analysis.",
		WorkerID:    "vyra/person_detection",
		Aliases:     []string{"vyra/person_detection", "vyra/object_detection"},
	},
	{
		Name:        "Situational Awareness",
		Instruction: "Monitor camera frames with VLM at reduced FPS. Save only problematic frames with a concise brief and AI analysis summary.",
		WorkerID:    "vyra/vlm",
		Aliases:     []string{"vyra/vlm", "vyra/wellness"},
	},
}

var legacyFaceAgentAliases = []string{"vyra/face_recognition", "vyra/face"}

func workerAliasMatch(workerID string, aliases []string) bool {
	for _, a := range aliases {
		if strings.EqualFold(workerID, a) {
			return true
		}
	}
	return false
}

func isLegacyFaceWorkerID(workerID string) bool {
	return workerAliasMatch(workerID, legacyFaceAgentAliases)
}

func matchAgentsForTemplate(allAgents []*Agent, tpl predefinedAgentTemplate) []*Agent {
	matches := make([]*Agent, 0, 2)
	for _, a := range allAgents {
		if workerAliasMatch(a.WorkerID, tpl.Aliases) {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		for _, a := range allAgents {
			if strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(tpl.Name)) {
				matches = append(matches, a)
			}
		}
	}
	return matches
}

func canonicalAgentTarget(matches []*Agent) *Agent {
	if len(matches) == 0 {
		return nil
	}
	target := matches[0]
	for _, m := range matches[1:] {
		if len(m.ServiceIDs) > len(target.ServiceIDs) {
			target = m
		}
	}
	return target
}

func mergeServiceIDs(groups ...[]string) []string {
	merged := make(map[string]struct{})
	for _, group := range groups {
		for _, sid := range group {
			sid = strings.TrimSpace(sid)
			if sid == "" {
				continue
			}
			merged[sid] = struct{}{}
		}
	}
	out := make([]string, 0, len(merged))
	for sid := range merged {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out
}
