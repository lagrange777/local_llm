package optimization

import "testing"

func TestProfilesAreBoundedAndDistinct(t *testing.T) {
	base, _ := Resolve("baseline", "frozen")
	opt, _ := Resolve("optimized-8b", "frozen")
	if base.Model != opt.Model || base.Options.NumCtx != 8192 || opt.Options.NumCtx != 4096 || opt.Options.NumPredict != 384 || opt.Prompt == base.Prompt {
		t.Fatal(base, opt)
	}
	q4, _ := Resolve("instruct-4b-q4", "frozen")
	q8, _ := Resolve("instruct-4b-q8", "frozen")
	if q4.Options != q8.Options || q4.Prompt != q8.Prompt || q4.ThinkSupported || q8.ThinkSupported {
		t.Fatal(q4, q8)
	}
	if _, err := Resolve("cloud:malicious", "frozen"); err == nil {
		t.Fatal("arbitrary model allowed")
	}
	if Fingerprint("frozen") == Fingerprint("changed") {
		t.Fatal("model excluded from fingerprint")
	}
}
