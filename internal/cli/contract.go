package cli

import (
	_ "embed"
	"strings"
)

// AgentContract is the operating contract `atask context` prints. It is
// static: no database, project, or session is required. Keep it between
// 300 and 1000 words so an agent can ingest it once.
//
//go:embed contract.txt
var AgentContract string

func agentContractWordCount() int {
	return len(strings.Fields(AgentContract))
}
