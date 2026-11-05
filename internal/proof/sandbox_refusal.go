package proof

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// OPTokenEnv is the 1Password service-account token. It is the one way a
// confined check can reach 1Password: the sandbox denies ~/.config/op and
// the Keychain, so the desktop integration is out of reach.
const OPTokenEnv = "OP_SERVICE_ACCOUNT_TOKEN"

// sshCommands read ~/.ssh (identity, known_hosts, config), which the check
// sandbox denies by design and no [sandbox] setting re-allows.
var sshCommands = []string{"ssh", "scp", "sftp", "ssh-add"}

// SandboxRefusal returns INVALID_INPUT, naming the grant it needs, when
// subject runs a command the check sandbox always refuses: the check could
// never pass, so it must not be filed. grantedEnv is the [sandbox]
// extra_env list. The caller applies it only to checks that run confined.
//
// It reads command words, so a tool that reaches ssh or op indirectly
// (a mise task, a script) is not seen; such a check fails at run time with
// the tool's own error.
func SandboxRefusal(subject string, grantedEnv []string) error {
	for _, word := range commandWords(subject, 0) {
		switch name := filepath.Base(word); {
		case slices.Contains(sshCommands, name):
			return fault.New(fault.CodeInvalidInput,
				"needs ~/.ssh: run %.60q calls %s, which reads your ssh identity and known hosts; the check sandbox denies reads of ~/.ssh and no [sandbox] setting grants them, so this check can never pass. Prove the outcome without a login (read the artifact from a local checkout or archive), or have the owner set [sandbox] mode = \"none\", which unconfines every check",
				subject, name)
		case name == "op" && !slices.Contains(grantedEnv, OPTokenEnv):
			return fault.New(fault.CodeInvalidInput,
				"needs %s: run %.60q calls op, and a confined check reaches 1Password only through a service-account token; the owner must add %q to [sandbox] extra_env in ~/.config/atask/config.toml",
				OPTokenEnv, subject, OPTokenEnv)
		}
	}
	return nil
}

// maxShellDepth bounds how many nested `sh -c` payloads commandWords opens.
const maxShellDepth = 4

// commandWordSkips are words that precede the command they run.
var commandWordSkips = []string{
	"if", "then", "else", "elif", "do", "while", "until", "!", "exec", "command", "nohup", "time", "env", "builtin",
}

var shells = []string{"sh", "bash", "zsh", "dash"}

// commandWords returns the command word of every simple command in script,
// looking through assignments, wrappers (env, exec, timeout ...) and the
// payload of `sh -c`.
func commandWords(script string, depth int) []string {
	var out []string
	for _, cmd := range simpleCommands(script) {
		i := commandIndex(cmd)
		if i >= len(cmd) {
			continue
		}
		word := cmd[i]
		out = append(out, word)
		if depth < maxShellDepth && slices.Contains(shells, filepath.Base(word)) {
			if payload, ok := shellPayload(cmd[i+1:]); ok {
				out = append(out, commandWords(payload, depth+1)...)
			}
		}
	}
	return out
}

// commandIndex is the position of the word that names the command: past
// leading VAR=value assignments and wrapper words.
func commandIndex(cmd []string) int {
	i := 0
	for i < len(cmd) {
		w := cmd[i]
		switch {
		case isAssignment(w), slices.Contains(commandWordSkips, w), strings.HasPrefix(w, "-") && i > 0:
			i++
		case w == "timeout":
			i++
			for i < len(cmd) && (strings.HasPrefix(cmd[i], "-") || startsWithDigit(cmd[i])) {
				i++
			}
		default:
			return i
		}
	}
	return i
}

func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// shellPayload is the script after a -c flag (-c, -lc, -ec ...).
func shellPayload(args []string) (string, bool) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// simpleCommands splits script into simple commands, each a list of
// unquoted words. Quotes group words; ; & | ( ) ` and newlines end a
// command outside quotes.
func simpleCommands(script string) [][]string {
	var (
		cmds    [][]string
		cmd     []string
		word    strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	endWord := func() {
		if inWord {
			cmd = append(cmd, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCommand := func() {
		endWord()
		if len(cmd) > 0 {
			cmds = append(cmds, cmd)
			cmd = nil
		}
	}
	for _, r := range script {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else {
				word.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			endWord()
		case strings.ContainsRune(";&|()`\n", r):
			endCommand()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endCommand()
	return cmds
}
