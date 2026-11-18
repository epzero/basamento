package expr

import "testing"

func TestCompileMappingRejectsNowInObject(t *testing.T) {
	_, err := CompileMapping("{t: now}", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsNowInArray(t *testing.T) {
	_, err := CompileMapping("[now]", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsEnvShorthand(t *testing.T) {
	_, err := CompileMapping("{$ENV}", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsNowInIf(t *testing.T) {
	_, err := CompileMapping("if true then now else 0 end", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsNowInTry(t *testing.T) {
	_, err := CompileMapping("try now catch 0", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsNowInLabel(t *testing.T) {
	_, err := CompileMapping("label $out | now", nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}

func TestCompileMappingRejectsNowInStringInterpolation(t *testing.T) {
	_, err := CompileMapping(`"\(now)"`, nil)
	if err == nil {
		t.Fatal("want compile error")
	}
}