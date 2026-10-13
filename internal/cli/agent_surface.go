package cli

// everydayAgentVerbs are the nine agent-facing commands named in
// docs/agent-contract.md and locked by the CLI surface tests.
var everydayAgentVerbs = []string{
	"add", "update", "claim", "log", "verify", "show", "list", "help", "version",
}

// unknownAgentVerbs must fail as unknown commands, not aliases of show, list, or log.
var unknownAgentVerbs = []string{"whoami", "ready", "comment"}
