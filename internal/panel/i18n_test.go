package panel

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func readCatalog(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile("../../static/i18n.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string][]string
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCatalogVerbsMatch(t *testing.T) {
	for k, v := range readCatalog(t) {
		want := verbRe.FindAllString(k, -1)
		if len(v) != 2 || v[0] == "" || v[1] == "" {
			t.Errorf("%q: need ja and en", k)
			continue
		}
		if hasHan(v[1]) {
			t.Errorf("%q: en still has CJK: %q", k, v[1])
		}
		for _, s := range v {
			seq := 0
			for _, m := range outVerbRe.FindAllStringSubmatch(s, -1) {
				n := seq
				if m[1] != "" {
					n, _ = strconv.Atoi(m[1])
					n--
				} else {
					seq++
				}
				if n >= len(want) || want[n][1] != m[0][len(m[0])-1] {
					t.Errorf("%q -> %q: verb %s doesn't match", k, s, m[0])
				}
			}
			if got := len(outVerbRe.FindAllString(s, -1)); got != len(want) {
				t.Errorf("%q -> %q: %d verbs, want %d", k, s, got, len(want))
			}
		}
	}
}

func TestCatalogCoversGoStrings(t *testing.T) {
	cat := readCatalog(t)
	files, _ := filepath.Glob("*.go")
	fs := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if ce, ok := n.(*ast.CallExpr); ok {
				if se, ok := ce.Fun.(*ast.SelectorExpr); ok {
					if id, ok := se.X.(*ast.Ident); ok && id.Name == "slog" {
						return false
					}
				}
			}
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err == nil && hasHan(s) {
				if _, ok := cat[s]; !ok {
					t.Errorf("%s: no translation for %q", fs.Position(bl.Pos()), s)
				}
			}
			return true
		})
	}
}

func TestCatalogCoversFrontend(t *testing.T) {
	cat := readCatalog(t)
	b, err := os.ReadFile("../../static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	lit := regexp.MustCompile(`'((?:[^'\\\n]|\\.)*)'`)
	for _, m := range lit.FindAllStringSubmatch(string(b), -1) {
		s := strings.ReplaceAll(m[1], `\'`, `'`)
		if !hasHan(s) || s == "中文" || s == "日本語" {
			continue
		}
		if _, ok := cat[s]; !ok {
			t.Errorf("app.js: no translation for %q", s)
		}
	}
}

func TestTranslate(t *testing.T) {
	old, oldP := catalog, patterns
	t.Cleanup(func() { catalog, patterns = old, oldP })
	setCatalog(map[string][]string{
		"ASF 连接":             {"ASF 接続", "ASF connections"},
		"在线状态":               {"オンライン状態", "Online status"},
		"挂卡时显示的文字":           {"ファーム中の表示テキスト", "Farming text"},
		"%s不存在，或你无权访问。":      {"%sが見つかりません。", "Not found: %s."},
		"作用范围：%s 中的 Bot %s。": {"対象：%s の Bot %s。", "Scope: bot %[2]s on %[1]s."},
		"这些设置刚被别人改过：%s。":     {"変更されました：%s。", "Changed: %s."},
		"一次最多 %d 个游戏。":       {"%d ゲームまで。", "Up to %d games."},
		"无法连接到 ASF。":         {"ASF に接続できません。", "Can't reach ASF."},
		"请检查地址、端口和防火墙设置。":    {"設定を確認してください。", "Check the settings."},
	})
	cases := []struct{ lang, in, want string }{
		{"en", "ASF 连接不存在，或你无权访问。", "Not found: ASF connections."},
		{"ja", "ASF 连接不存在，或你无权访问。", "ASF 接続が見つかりません。"},
		{"en", "作用范围：home 中的 Bot b1。", "Scope: bot b1 on home."},
		{"en", "这些设置刚被别人改过：在线状态、挂卡时显示的文字。", "Changed: Online status, Farming text."},
		{"en", "一次最多 31 个游戏。", "Up to 31 games."},
		{"en", "无法连接到 ASF。\n请检查地址、端口和防火墙设置。", "Can't reach ASF.\nCheck the settings."},
		{"zh", "无法连接到 ASF。", "无法连接到 ASF。"},
		{"en", "bot-name", "bot-name"},
		{"en", "没收录的句子", "没收录的句子"},
	}
	for _, c := range cases {
		if got := tr(c.lang, c.in); got != c.want {
			t.Errorf("tr(%s, %q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
	out := localize("en", M{"error": M{"message": "无法连接到 ASF。", "count": int64(9007199254740993)}, "list": []M{{"x": "ASF 连接"}}})
	b, _ := json.Marshal(out)
	if want := `{"error":{"count":9007199254740993,"message":"Can't reach ASF."},"list":[{"x":"ASF connections"}]}`; string(b) != want {
		t.Errorf("localize = %s", b)
	}
}
