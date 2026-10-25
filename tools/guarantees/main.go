// guarantees は、保証の対応表（spec/guarantees.toml）を仕様・DB の構造・テストと突き合わせて確かめ、人が読む Markdown の表を出す。
// CLAUDE.md の「保証の対応表」の「抜き出して確かめる道具」と「変異テスト」を行う。依存を増やさないため、Go の標準ライブラリだけで書く。
//
// 使い方（リポジトリの根から実行する）:
//
//	guarantees [check]               表を確かめ、Markdown の表を標準出力に出す。満たさない点があれば標準エラー出力に並べ、終了コード 1
//	guarantees fingerprint           仕様の宣言と ensures ごとの指紋を出す（表の fingerprint に書く値）
//	guarantees mutation-targets      変異テストの対象外にするディレクトリを、Gremlins の --exclude-files の引数の形で 1 行ずつ出す
//	guarantees mutation-check FILE   Gremlins の結果（JSON）を spec/mutation-exclusions.toml と突き合わせる。除外していない生き残りがあれば終了コード 1
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	specDir        = "spec"
	tablePath      = "spec/guarantees.toml"
	exclusionsPath = "spec/mutation-exclusions.toml"
	schemaPath     = "db/schema.sql"
	generatedDir   = "internal/spec/"
)

var (
	importances = map[string]int{"低": 1, "中": 2, "高": 3}
	guardWords  = []string{"証明", "DB の制約", "実行時の確認", "テスト", "人のレビュー", "なし"}
)

func main() {
	args := os.Args[1:]
	cmd := "check"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "check":
		err = runCheck()
	case "fingerprint":
		err = runFingerprint()
	case "mutation-targets":
		err = runMutationTargets()
	case "mutation-check":
		if len(args) != 1 {
			err = errors.New("使い方: guarantees mutation-check <Gremlins の結果の JSON>")
		} else {
			err = runMutationCheck(args[0])
		}
	default:
		err = fmt.Errorf("知らないサブコマンド: %s", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---------- 仕様（Dafny）を読む ----------

type decl struct {
	kind     string // lemma・function・predicate・method
	name     string
	meaning  string // 宣言の直前のコメント
	requires []string
	ensures  []string
	header   string // 宣言の行から本体の手前まで（節を含む）
	body     string
}

var (
	declRe   = regexp.MustCompile(`^\s*(?:ghost\s+)?(lemma|function|predicate|method)\s+(?:\{:[^}]*\}\s*)*([A-Za-z_][A-Za-z0-9_']*)`)
	clauseRe = regexp.MustCompile(`^\s*(requires|ensures|decreases|reads|modifies)\b`)
)

// readSpec は、spec の下の .dfy ファイルから宣言を読む。
// 宣言の本体の始まりの { は、行の初めに置く書き方を前提にする（そう書かれていなければ失敗する）
func readSpec() (map[string]*decl, error) {
	files, err := filepath.Glob(filepath.Join(specDir, "*.dfy"))
	if err != nil {
		return nil, err
	}
	decls := map[string]*decl{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		for i := 0; i < len(lines); i++ {
			m := declRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			d := &decl{kind: m[1], name: m[2], meaning: commentAbove(lines, i)}
			var header []string
			var cur *string
			j := i
			for ; j < len(lines); j++ {
				t := strings.TrimSpace(stripComment(lines[j]))
				if j > i && strings.HasPrefix(t, "{") {
					break
				}
				if j > i && declRe.MatchString(lines[j]) {
					return nil, fmt.Errorf("%s:%d: 宣言 %s の本体の { が見つからない（{ は行の初めに置く）", f, i+1, d.name)
				}
				header = append(header, lines[j])
				if cm := clauseRe.FindStringSubmatch(lines[j]); cm != nil {
					text := strings.TrimSpace(strings.TrimPrefix(t, cm[1]))
					switch cm[1] {
					case "requires":
						d.requires = append(d.requires, text)
						cur = &d.requires[len(d.requires)-1]
					case "ensures":
						d.ensures = append(d.ensures, text)
						cur = &d.ensures[len(d.ensures)-1]
					default:
						cur = nil
					}
				} else if cur != nil {
					*cur += " " + t
				}
			}
			if j == len(lines) {
				return nil, fmt.Errorf("%s:%d: 宣言 %s の本体の { が見つからない", f, i+1, d.name)
			}
			bodyEnd, err := matchBrace(lines, j)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %s: %w", f, j+1, d.name, err)
			}
			d.header = strings.Join(header, "\n")
			d.body = strings.Join(lines[j:bodyEnd+1], "\n")
			if _, dup := decls[d.name]; dup {
				return nil, fmt.Errorf("%s:%d: 宣言の名前 %s が重なっている", f, i+1, d.name)
			}
			decls[d.name] = d
			i = bodyEnd
		}
	}
	return decls, nil
}

func stripComment(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		return s[:i]
	}
	return s
}

func commentAbove(lines []string, i int) string {
	var out []string
	for k := i - 1; k >= 0; k-- {
		t := strings.TrimSpace(lines[k])
		if !strings.HasPrefix(t, "//") {
			break
		}
		out = append([]string{strings.TrimSpace(strings.TrimPrefix(t, "//"))}, out...)
	}
	return strings.Join(out, " ")
}

// matchBrace は、start の行の { に対応する } の行を返す
func matchBrace(lines []string, start int) (int, error) {
	depth := 0
	for k := start; k < len(lines); k++ {
		for _, r := range stripComment(lines[k]) {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth == 0 {
			return k, nil
		}
	}
	return 0, errors.New("本体の終わりの } が見つからない")
}

// fingerprint は、空白とコメントの違いを除いた本文の指紋
func fingerprint(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		for _, line := range strings.Split(p, "\n") {
			for _, r := range stripComment(line) {
				if r != ' ' && r != '\t' && r != '\r' {
					b.WriteRune(r)
				}
			}
		}
		b.WriteString("\x00")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// ensuresFingerprint は、ensures i の指紋。ensures の意味は requires に左右されるので、requires も含める
func (d *decl) ensuresFingerprint(i int) string {
	return fingerprint("requires", strings.Join(d.requires, "\n"), "ensures", d.ensures[i])
}

// wholeFingerprint は、宣言全体の指紋。ensures があればすべての requires と ensures、なければ宣言の本文（述語の定義など）
func (d *decl) wholeFingerprint() string {
	if len(d.ensures) > 0 {
		return fingerprint("requires", strings.Join(d.requires, "\n"), "ensures", strings.Join(d.ensures, "\n"))
	}
	return fingerprint(d.header, d.body)
}

// ---------- 表（TOML のうち、ここで使う形だけ）を読む ----------

type value struct {
	str   string
	num   int
	list  []string
	isNum bool
}

type table map[string]value

// readTables は、[[name]] の表の並びを読む。値は、文字列・整数・1 行に収めた文字列の配列だけを受け付ける
func readTables(path, name string) ([]table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []table
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			if line != "[["+name+"]]" {
				return nil, fmt.Errorf("%s:%d: 知らない表 %s", path, n, line)
			}
			out = append(out, table{})
			continue
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s:%d: [[%s]] の前に値がある", path, n, name)
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: key = value の形でない", path, n)
		}
		key = strings.TrimSpace(key)
		v, err := parseValue(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s: %w", path, n, key, err)
		}
		if _, dup := out[len(out)-1][key]; dup {
			return nil, fmt.Errorf("%s:%d: %s が重なっている", path, n, key)
		}
		out[len(out)-1][key] = v
	}
	return out, sc.Err()
}

func parseValue(s string) (value, error) {
	switch {
	case strings.HasPrefix(s, `"`):
		str, rest, err := parseString(s)
		if err != nil {
			return value{}, err
		}
		if !emptyOrComment(rest) {
			return value{}, fmt.Errorf("文字列の後に余分なもの: %q", rest)
		}
		return value{str: str}, nil
	case strings.HasPrefix(s, "["):
		var list []string
		rest := strings.TrimSpace(s[1:])
		for !strings.HasPrefix(rest, "]") {
			str, r, err := parseString(rest)
			if err != nil {
				return value{}, fmt.Errorf("配列は 1 行に収めた文字列だけ: %w", err)
			}
			list = append(list, str)
			rest = strings.TrimSpace(r)
			if strings.HasPrefix(rest, ",") {
				rest = strings.TrimSpace(rest[1:])
			} else if !strings.HasPrefix(rest, "]") {
				return value{}, fmt.Errorf("配列の区切りがない: %q", rest)
			}
		}
		if !emptyOrComment(rest[1:]) {
			return value{}, fmt.Errorf("配列の後に余分なもの: %q", rest[1:])
		}
		return value{list: list}, nil
	default:
		num, comment, _ := strings.Cut(s, "#")
		_ = comment
		i, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil {
			return value{}, fmt.Errorf("値は文字列・整数・文字列の配列のどれか: %q", s)
		}
		return value{num: i, isNum: true}, nil
	}
}

func parseString(s string) (string, string, error) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", fmt.Errorf("文字列でない: %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 >= len(s) {
				return "", "", errors.New("文字列が \\ で終わっている")
			}
			i++
			switch s[i] {
			case '"', '\\':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			default:
				return "", "", fmt.Errorf("知らないエスケープ \\%c", s[i])
			}
		case '"':
			return b.String(), s[i+1:], nil
		default:
			b.WriteByte(s[i])
		}
	}
	return "", "", errors.New("文字列が閉じていない")
}

func emptyOrComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}

// ---------- 保証の表 ----------

type guarantee struct {
	id, property, fingerprint, importance, review, approved, rationale string
	ensures                                                            int // 宣言全体なら -1
	guards, proofs, constraints, premises, code                        []string
}

var knownKeys = []string{"id", "property", "ensures", "fingerprint", "importance", "guards", "proofs", "constraints", "premises", "code", "review", "approved", "rationale"}

func readGuarantees() ([]guarantee, error) {
	tables, err := readTables(tablePath, "guarantee")
	if err != nil {
		return nil, err
	}
	var out []guarantee
	for i, t := range tables {
		for k := range t {
			if !slices.Contains(knownKeys, k) {
				return nil, fmt.Errorf("%s の %d 番目の行: 知らない項目 %s", tablePath, i+1, k)
			}
		}
		g := guarantee{
			id: t["id"].str, property: t["property"].str, fingerprint: t["fingerprint"].str,
			importance: t["importance"].str, review: t["review"].str, approved: t["approved"].str,
			rationale: t["rationale"].str, ensures: -1,
			guards: t["guards"].list, proofs: t["proofs"].list, constraints: t["constraints"].list,
			premises: t["premises"].list, code: t["code"].list,
		}
		if v, ok := t["ensures"]; ok {
			if !v.isNum {
				return nil, fmt.Errorf("%s の行 %s: ensures は整数", tablePath, g.id)
			}
			g.ensures = v.num
		}
		out = append(out, g)
	}
	return out, nil
}

// testMarkers は、テストの関数の直前の「// 守る: id, id」の印を集める（行の id → テストの名前）
func testMarkers() (map[string][]string, []string, error) {
	markers := map[string][]string{}
	var problems []string
	funcRe := regexp.MustCompile(`^func\s+(Test[^\s(]*)\s*\(`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == strings.TrimSuffix(generatedDir, "/") || strings.HasPrefix(d.Name(), ".") && path != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			t := strings.TrimSpace(line)
			if !strings.HasPrefix(t, "// 守る:") {
				continue
			}
			k := i + 1
			for k < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[k]), "//") {
				k++
			}
			m := funcRe.FindStringSubmatch(func() string {
				if k < len(lines) {
					return lines[k]
				}
				return ""
			}())
			if m == nil {
				problems = append(problems, fmt.Sprintf("%s:%d: 「// 守る:」の印の直後がテストの関数でない", path, i+1))
				continue
			}
			for _, id := range strings.Split(strings.TrimPrefix(t, "// 守る:"), ",") {
				id = strings.TrimSpace(id)
				if id != "" {
					markers[id] = append(markers[id], m[1])
				}
			}
		}
		return nil
	})
	return markers, problems, err
}

// schemaNames は、db/schema.sql の制約・トリガー・索引の名前
func schemaNames() (map[string]bool, error) {
	b, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?i)\b(?:CONSTRAINT|TRIGGER(?:\s+IF\s+NOT\s+EXISTS)?|INDEX(?:\s+IF\s+NOT\s+EXISTS)?)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	names := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			names[m[1]] = true
		}
	}
	return names, nil
}

func runCheck() error {
	decls, err := readSpec()
	if err != nil {
		return err
	}
	gs, err := readGuarantees()
	if err != nil {
		return err
	}
	markers, problems, err := testMarkers()
	if err != nil {
		return err
	}
	names, err := schemaNames()
	if err != nil {
		return err
	}
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	byID := map[string]guarantee{}
	covered := map[string]bool{} // "名前#ensures の番号" または "名前#*"
	for _, g := range gs {
		if g.id == "" {
			bad("id のない行がある（property=%s）", g.property)
			continue
		}
		if _, dup := byID[g.id]; dup {
			bad("行の id %s が重なっている", g.id)
		}
		byID[g.id] = g
		d, ok := decls[g.property]
		if !ok {
			bad("%s: 仕様に宣言 %s がない", g.id, g.property)
			continue
		}
		var want string
		if g.ensures >= 0 {
			if g.ensures >= len(d.ensures) {
				bad("%s: %s の ensures は %d 個しかない（ensures = %d）", g.id, g.property, len(d.ensures), g.ensures)
				continue
			}
			want = d.ensuresFingerprint(g.ensures)
			covered[fmt.Sprintf("%s#%d", g.property, g.ensures)] = true
		} else {
			want = d.wholeFingerprint()
			covered[g.property+"#*"] = true
		}
		if g.fingerprint != want {
			bad("%s: 指紋が今の仕様と合わない（表 %s、仕様 %s）。本文が変わったなら、守り方が今も正しいかを見直してから直す", g.id, g.fingerprint, want)
		}
		if _, ok := importances[g.importance]; !ok {
			bad("%s: 重要度 %q は 高・中・低 のどれでもない", g.id, g.importance)
		}
		if len(g.guards) == 0 {
			bad("%s: guards がない", g.id)
		}
		for _, w := range g.guards {
			if !slices.Contains(guardWords, w) {
				bad("%s: 守るもの %q は決まった言葉でない", g.id, w)
			}
		}
		has := func(w string) bool { return slices.Contains(g.guards, w) }
		if has("証明") && len(g.proofs) == 0 {
			bad("%s: 証明で守るのに proofs がない", g.id)
		}
		for _, p := range g.proofs {
			if _, ok := decls[p]; !ok {
				bad("%s: proofs の %s が仕様にない", g.id, p)
			}
		}
		if has("DB の制約") && len(g.constraints) == 0 {
			bad("%s: DB の制約で守るのに constraints がない", g.id)
		}
		for _, c := range g.constraints {
			if !names[c] {
				bad("%s: constraints の %s が %s にない", g.id, c, schemaPath)
			}
		}
		if has("人のレビュー") && g.review == "" {
			bad("%s: 人のレビューで守るのに review がない", g.id)
		}
		if (has("テスト") || has("DB の制約") || has("実行時の確認")) && len(markers[g.id]) == 0 {
			bad("%s: 「// 守る: %s」の印を付けたテストがない", g.id, g.id)
		}
		if g.rationale == "" {
			bad("%s: rationale がない", g.id)
		}
		// 重要度に必要な守り方（CLAUDE.md の重要度の表）
		strong := has("証明") || has("DB の制約")
		switch g.importance {
		case "高":
			if has("なし") {
				bad("%s: 重要度 高に「なし」は認めない", g.id)
			} else if !strong && g.approved == "" {
				bad("%s: 重要度 高を証明でも DB の制約でもなく守るなら、approved（人が了承した日付）が要る", g.id)
			}
		case "中":
			if has("なし") {
				bad("%s: 重要度 中に「なし」は認めない", g.id)
			} else if !(strong || has("実行時の確認") || has("テスト")) {
				bad("%s: 重要度 中は、証明・DB の制約・実行時の確認・テストのどれかで守る", g.id)
			}
		case "低":
			if has("なし") && g.approved == "" {
				bad("%s: 「なし」には approved（人が了承した日付）が要る", g.id)
			}
		}
	}
	for id := range markers {
		if _, ok := byID[id]; !ok {
			bad("テスト %v の「// 守る: %s」が、表にない行を指している", markers[id], id)
		}
	}
	for _, g := range gs {
		for _, p := range g.premises {
			pg, ok := byID[p]
			if !ok {
				bad("%s: 前提 %s が表にない", g.id, p)
				continue
			}
			if importances[pg.importance] < importances[g.importance] {
				bad("%s: 前提 %s の重要度（%s）が、この行の重要度（%s）より低い", g.id, p, pg.importance, g.importance)
			}
		}
	}
	// すべての補題と、ensures を持つ関数・述語・メソッドの、すべての ensures が、どれかの行で扱われている
	var declNames []string
	for n := range decls {
		declNames = append(declNames, n)
	}
	sort.Strings(declNames)
	for _, n := range declNames {
		d := decls[n]
		if d.kind != "lemma" && len(d.ensures) == 0 {
			continue
		}
		if covered[n+"#*"] {
			continue
		}
		if len(d.ensures) == 0 {
			bad("補題 %s を扱う行がない", n)
		}
		for i := range d.ensures {
			if !covered[fmt.Sprintf("%s#%d", n, i)] {
				bad("%s の ensures %d を扱う行がない", n, i)
			}
		}
	}

	printMarkdown(gs, decls, markers)
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("保証の対応表が満たしていない点が %d 個ある:\n- %s", len(problems), strings.Join(problems, "\n- "))
	}
	return nil
}

func printMarkdown(gs []guarantee, decls map[string]*decl, markers map[string][]string) {
	esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }
	fmt.Println("| id | 性質 | 意味 | 重要度 | 守るもの | 証明 | 制約 | 前提 | テスト | 了承 | 根拠 |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, g := range gs {
		prop := g.property
		meaning := ""
		if d, ok := decls[g.property]; ok {
			meaning = d.meaning
		}
		if g.ensures >= 0 {
			prop = fmt.Sprintf("%s（ensures %d）", g.property, g.ensures)
		}
		tests := slices.Clone(markers[g.id])
		sort.Strings(tests)
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			g.id, prop, esc(meaning), g.importance, strings.Join(g.guards, "、"), strings.Join(g.proofs, "、"),
			strings.Join(g.constraints, "、"), strings.Join(g.premises, "、"), strings.Join(tests, "、"), g.approved, esc(g.rationale))
	}
}

func runFingerprint() error {
	decls, err := readSpec()
	if err != nil {
		return err
	}
	var names []string
	for n := range decls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := decls[n]
		fmt.Printf("%s\t*\t%s\n", n, d.wholeFingerprint())
		for i := range d.ensures {
			fmt.Printf("%s\t%d\t%s\n", n, i, d.ensuresFingerprint(i))
		}
	}
	return nil
}

// ---------- 変異テスト ----------

// mutationTargets は、変異テストの対象にする Go のコードのディレクトリ。
// 重要度が高の行と前提の行のうち、テスト・DB の制約・実行時の確認で守る行の code
func mutationTargets() ([]string, error) {
	gs, err := readGuarantees()
	if err != nil {
		return nil, err
	}
	premise := map[string]bool{}
	for _, g := range gs {
		for _, p := range g.premises {
			premise[p] = true
		}
	}
	set := map[string]bool{}
	for _, g := range gs {
		if g.importance != "高" && !premise[g.id] {
			continue
		}
		if !(slices.Contains(g.guards, "テスト") || slices.Contains(g.guards, "DB の制約") || slices.Contains(g.guards, "実行時の確認")) {
			continue
		}
		for _, c := range g.code {
			set[strings.TrimSuffix(c, "/")] = true
		}
	}
	var out []string
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}

func runMutationTargets() error {
	targets, err := mutationTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return errors.New("変異テストの対象がない")
	}
	// Go のコードがあるディレクトリのうち、対象でないものを外す
	dirs := map[string]bool{}
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != "." {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			dirs[filepath.ToSlash(filepath.Dir(path))] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	var excluded []string
	for dir := range dirs {
		if !slices.Contains(targets, dir) {
			excluded = append(excluded, dir)
		}
	}
	sort.Strings(excluded)
	for _, dir := range excluded {
		if dir == "." {
			fmt.Println(`--exclude-files=^[^/]*\.go$`)
		} else {
			fmt.Printf("--exclude-files=^%s/\n", regexp.QuoteMeta(dir))
		}
	}
	return nil
}

type gremlinsResult struct {
	Files []struct {
		FileName  string `json:"file_name"`
		Mutations []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Line   int    `json:"line"`
			Column int    `json:"column"`
		} `json:"mutations"`
	} `json:"files"`
}

func runMutationCheck(resultPath string) error {
	b, err := os.ReadFile(resultPath)
	if err != nil {
		return err
	}
	var r gremlinsResult
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("%s を読めない: %w", resultPath, err)
	}
	exclusions, err := readTables(exclusionsPath, "exclusion")
	if err != nil {
		return err
	}
	for i, e := range exclusions {
		if e["file"].str == "" || e["type"].str == "" || e["code"].str == "" || e["reason"].str == "" {
			return fmt.Errorf("%s の %d 番目: file・type・code・reason がそろっていない", exclusionsPath, i+1)
		}
	}
	used := make([]bool, len(exclusions))
	var survivors []string
	total := 0
	for _, f := range r.Files {
		src, err := os.ReadFile(f.FileName)
		if err != nil {
			return err
		}
		lines := strings.Split(string(src), "\n")
		for _, m := range f.Mutations {
			total++
			if m.Status != "LIVED" && m.Status != "NOT COVERED" {
				continue
			}
			code := ""
			if m.Line-1 < len(lines) {
				code = strings.TrimSpace(lines[m.Line-1])
			}
			excluded := false
			for i, e := range exclusions {
				if e["file"].str == f.FileName && e["type"].str == m.Type && e["code"].str == code {
					used[i] = true
					excluded = true
				}
			}
			if !excluded {
				survivors = append(survivors, fmt.Sprintf("%s:%d:%d %s %s: %s", f.FileName, m.Line, m.Column, m.Status, m.Type, code))
			}
		}
	}
	if total == 0 {
		return errors.New("変異が 1 つも作られていない（対象の指定を確かめる）")
	}
	var problems []string
	for i, u := range used {
		if !u {
			problems = append(problems, fmt.Sprintf("%s の除外（%s %s %q）に当たる変異がない。コードが変わったなら除外を見直す",
				exclusionsPath, exclusions[i]["file"].str, exclusions[i]["type"].str, exclusions[i]["code"].str))
		}
	}
	if len(survivors) > 0 {
		problems = append(problems, "テストが落ちなかった変異:\n  "+strings.Join(survivors, "\n  "))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	fmt.Printf("変異 %d 個のうち、生き残りは除外したものだけ\n", total)
	return nil
}
