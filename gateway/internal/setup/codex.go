package setup

// Codex CLI reads ~/.codex/config.toml (or $CODEX_HOME/config.toml). Setup
// adds a custom model provider pointing at the gateway and selects it:
//
//	model_provider = "rlcd_gateway"
//
//	[model_providers.rlcd_gateway]
//	name = "RLCD Gateway"
//	base_url = "http://127.0.0.1:4777/openai/v1"
//	wire_api = "responses"
//	requires_openai_auth = true
//	supports_websockets = false
//
// requires_openai_auth = true (and no env_key) makes Codex send its own
// login, whether that is a ChatGPT subscription (OAuth token plus
// ChatGPT-Account-ID) or an API key stored by `codex login --with-api-key`;
// base_url is honoured for either. The gateway forwards subscription calls
// to chatgpt.com/backend-api/codex and API-key calls to api.openai.com/v1.
// The built-in "openai" provider cannot be redefined, and it always tries
// WebSockets first, hence a custom provider with supports_websockets = false.
//
// Sources:
//   https://learn.chatgpt.com/docs/config-file/config-reference (model_providers, openai_base_url, chatgpt_base_url)
//   https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs (base_url wins over the ChatGPT default; reserved ids; wire_api "chat" removed)
//   https://coder.com/docs/ai-coder/ai-gateway/clients/codex (subscription through a gateway with requires_openai_auth)
//
// TOML is edited without a TOML library, as text, in two delimited blocks:
//
//   - the model_provider line. A top-level key must come before the first
//     table, so the block goes at the top of the file, or in place of an
//     existing top-level model_provider line, which is kept in the block
//     verbatim ("# rlcd-gateway-was: ...") so undo restores it exactly;
//   - the provider table, appended at the end of the file.
//
// Undo removes both blocks and restores what they replaced, byte for byte.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexProviderID = "rlcd_gateway"

	markTopBegin = "# >>> rlcd-gateway: model_provider >>>"
	markTopEnd   = "# <<< rlcd-gateway: model_provider <<<"
	markTabBegin = "# >>> rlcd-gateway: provider table >>>"
	markTabEnd   = "# <<< rlcd-gateway: provider table <<<"
	markWas      = "# rlcd-gateway-was: "
	flagNoEOL    = " (no final newline)"
)

func codexHome() (string, error) {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func codexConfigPath() (string, error) {
	d, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// CodexBaseURL is what the Codex provider's base_url is set to.
func CodexBaseURL(gatewayURL string) string { return strings.TrimRight(gatewayURL, "/") + "/openai/v1" }

// CodexTryCommand runs Codex through the gateway once, without editing config.toml.
func CodexTryCommand(gatewayURL string) string {
	return fmt.Sprintf(`codex -c model_provider=%s -c 'model_providers.%s={name="RLCD Gateway", base_url="%s", wire_api="responses", requires_openai_auth=true, supports_websockets=false}'`,
		codexProviderID, codexProviderID, CodexBaseURL(gatewayURL))
}

// Codex points Codex CLI at the gateway. It returns the file it changed.
func Codex(gatewayURL string) (string, error) {
	path, err := codexConfigPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	out, err := codexApply(string(b), gatewayURL)
	if err != nil {
		return "", fmt.Errorf("%s: %w; refusing to touch it", path, err)
	}
	if exists {
		if err := writeBackup(path, b); err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, writeKeepMode(path, []byte(out))
}

// UndoCodex removes what Codex added and restores the replaced line.
func UndoCodex() (string, error) {
	path, err := codexConfigPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	out, changed, err := codexRemove(string(b))
	if err != nil {
		return "", fmt.Errorf("%s: %w; refusing to touch it", path, err)
	}
	if !changed {
		return path, nil
	}
	if err := writeBackup(path, b); err != nil {
		return "", err
	}
	return path, writeKeepMode(path, []byte(out))
}

func newlineOf(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func lines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func bare(line string) string { return strings.TrimRight(line, "\r\n") }

// codexRemove strips the managed blocks, restoring what they replaced.
// changed=false means there was nothing of ours in the file.
func codexRemove(src string) (out string, changed bool, err error) {
	ls := lines(src)
	var b strings.Builder
	for i := 0; i < len(ls); i++ {
		l := bare(ls[i])
		top := strings.HasPrefix(l, markTopBegin)
		tab := strings.HasPrefix(l, markTabBegin)
		if !top && !tab {
			b.WriteString(ls[i])
			continue
		}
		end := markTopEnd
		if tab {
			end = markTabEnd
		}
		noEOL := strings.HasSuffix(l, flagNoEOL)
		j := i + 1
		var was []string
		for ; j < len(ls) && bare(ls[j]) != end; j++ {
			if w, ok := strings.CutPrefix(bare(ls[j]), markWas); ok {
				was = append(was, w)
			}
		}
		if j == len(ls) {
			return "", false, fmt.Errorf("line %d: rlcd-gateway block has no end marker", i+1)
		}
		changed = true
		nl := "\n"
		if strings.HasSuffix(ls[i], "\r\n") {
			nl = "\r\n"
		}
		if top {
			for k, w := range was {
				b.WriteString(w)
				if k < len(was)-1 || !noEOL {
					b.WriteString(nl)
				}
			}
		} else if noEOL {
			// We added a newline before the block; take it back.
			s := strings.TrimSuffix(b.String(), nl)
			b.Reset()
			b.WriteString(s)
		}
		i = j
	}
	return b.String(), changed, nil
}

// codexApply returns src with the gateway provider selected. It is
// idempotent: existing managed blocks are undone first, so the original
// model_provider line is never lost.
func codexApply(src, gatewayURL string) (string, error) {
	pristine, _, err := codexRemove(src)
	if err != nil {
		return "", err
	}
	stmts, err := parseTOML(pristine)
	if err != nil {
		return "", fmt.Errorf("cannot parse TOML (%w)", err)
	}
	var current *tomlStmt
	for i, s := range stmts {
		if s.header {
			if hasPrefix(s.path, "model_providers", codexProviderID) {
				return "", fmt.Errorf("it already defines [model_providers.%s]", codexProviderID)
			}
			continue
		}
		full := s.full()
		if hasPrefix(full, "model_providers", codexProviderID) || pathEq(full, "model_providers") {
			return "", fmt.Errorf("line %d: model_providers is defined in a form we cannot extend safely", s.startLine+1)
		}
		if len(s.path) == 0 && pathEq(s.key, "model_provider") {
			current = &stmts[i]
		}
	}

	nl := newlineOf(pristine)
	ls := lines(pristine)
	var top strings.Builder
	begin := markTopBegin
	if current != nil && !strings.HasSuffix(ls[current.endLine], "\n") {
		begin += flagNoEOL
	}
	top.WriteString(begin + nl)
	top.WriteString("# Managed by rlcd-gateway. `rlcd-gateway undo codex` restores the previous setting." + nl)
	if current != nil {
		for k := current.startLine; k <= current.endLine; k++ {
			top.WriteString(markWas + bare(ls[k]) + nl)
		}
	}
	top.WriteString(`model_provider = "` + codexProviderID + `"` + nl)
	top.WriteString(markTopEnd + nl)

	var b strings.Builder
	var rest []string
	if current != nil {
		for _, l := range ls[:current.startLine] {
			b.WriteString(l)
		}
		b.WriteString(top.String())
		rest = ls[current.endLine+1:]
	} else {
		b.WriteString(top.String())
		rest = ls
	}
	for _, l := range rest {
		b.WriteString(l)
	}

	body := b.String()
	tabBegin := markTabBegin
	if !strings.HasSuffix(body, "\n") {
		// Only possible when the file ends without a newline.
		tabBegin += flagNoEOL
		body += nl
	}
	body += tabBegin + nl +
		"# Managed by rlcd-gateway. `rlcd-gateway undo codex` removes this block." + nl +
		"[model_providers." + codexProviderID + "]" + nl +
		`name = "RLCD Gateway"` + nl +
		`base_url = ` + strconvQuote(CodexBaseURL(gatewayURL)) + nl +
		`wire_api = "responses"` + nl +
		"requires_openai_auth = true" + nl +
		"supports_websockets = false" + nl +
		markTabEnd + nl
	if _, err := parseTOML(body); err != nil {
		return "", fmt.Errorf("internal error, edited file would not parse (%w)", err)
	}
	return body, nil
}

func strconvQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// codexState reads what Codex currently uses.
type codexState struct {
	provider string // "" means the built-in default ("openai")
	baseURL  string // the selected provider's base_url, or openai_base_url
	profile  string
}

func readCodex(src string) (codexState, error) {
	stmts, err := parseTOML(src)
	if err != nil {
		return codexState{}, err
	}
	var st codexState
	vals := map[string]string{}
	for _, s := range stmts {
		if s.header {
			continue
		}
		if v, err := unquoteTOML(s.value); err == nil {
			vals[strings.Join(s.full(), "\x00")] = v
		}
	}
	st.provider = vals["model_provider"]
	st.profile = vals["profile"]
	if st.provider == "" || st.provider == "openai" {
		st.baseURL = vals["openai_base_url"]
	} else {
		st.baseURL = vals["model_providers\x00"+st.provider+"\x00base_url"]
	}
	return st, nil
}

// pointsAt reports whether u addresses the gateway (same port on a loopback host).
func pointsAt(u, gatewayURL string) bool {
	a, err1 := url.Parse(u)
	g, err2 := url.Parse(gatewayURL)
	if err1 != nil || err2 != nil || a.Host == "" {
		return false
	}
	norm := func(h string) string {
		if h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") {
			return "loopback"
		}
		return h
	}
	return norm(a.Hostname()) == norm(g.Hostname()) && a.Port() == g.Port()
}
