package main

import (
	"strings"
	"testing"
)

// undefined 标准库：应补进现有 import 块，且不重复。
func TestAutoFixAddStdImport(t *testing.T) {
	src := "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {\n\tfmt.Println(1)\n\t_ = os.Args\n}\n"
	stderr := "# command-line-arguments\n./main.go:8:5: undefined: os\n"
	out, ok := autoFixGoImports(src, stderr)
	if !ok {
		t.Fatal("应当发生修补")
	}
	if !strings.Contains(out, "\"os\"") || !strings.Contains(out, "\"fmt\"") {
		t.Fatalf("补 import 错误:\n%s", out)
	}
}

// 没有 import 块时应新建一个；encoding/json 这种深路径也要对。
func TestAutoFixNewImportBlock(t *testing.T) {
	src := "package main\n\nfunc main() {\n\t_ = json.Marshal\n}\n"
	out, ok := autoFixGoImports(src, "undefined: json")
	if !ok || !strings.Contains(out, "import (\n\t\"encoding/json\"\n)") {
		t.Fatalf("新建 import 块错误:\n%s", out)
	}
}

// imported and not used：块式与单行都要删。
func TestAutoFixRemoveUnused(t *testing.T) {
	src := "package main\n\nimport (\n\t\"fmt\"\n\t\"time\"\n)\n\nfunc main() {\n\t_ = time.Now\n}\n"
	out, ok := autoFixGoImports(src, `imported and not used: "fmt"`)
	if !ok || strings.Contains(out, "\"fmt\"") || !strings.Contains(out, "\"time\"") {
		t.Fatalf("删未用 import 错误:\n%s", out)
	}
	single := "package main\n\nimport \"fmt\"\n\nfunc main() {}\n"
	out2, ok2 := autoFixGoImports(single, `imported and not used: "fmt"`)
	if !ok2 || strings.Contains(out2, "import \"fmt\"") {
		t.Fatalf("删单行 import 错误:\n%s", out2)
	}
}

// 非标准库的 undefined 不动。
func TestAutoFixUnknownUnchanged(t *testing.T) {
	src := "package main\n\nfunc main() {\n\tfoo.Bar()\n}\n"
	if _, ok := autoFixGoImports(src, "undefined: foo"); ok {
		t.Fatal("第三方/未知标识符不应自动补")
	}
	// 没有匹配报错也不应标记改动
	if _, ok := autoFixGoImports(src, "syntax error: lol"); ok {
		t.Fatal("无 import 类报错不应改动")
	}
}
