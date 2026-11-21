package tool

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/scheduler"
)

func TestProcessStreamWriterTail(t *testing.T) {
	const tailMax = 64 << 10
	big := make([]byte, 1<<20)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	big[len(big)-1] = 'Z'
	cases := []struct {
		name   string
		chunks [][]byte
	}{
		{name: "one 1 MiB write", chunks: [][]byte{big}},
		{name: "many small writes", chunks: splitEvery(big, 4093)},
		{name: "writes straddling the window", chunks: splitEvery(big, tailMax-7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reported bytes.Buffer
			w := newProcessStreamWriter(4<<10, "id", ProcessStreamStdout, func(_, _, data string) { reported.WriteString(data) })
			w.tailMax = tailMax
			for _, c := range tc.chunks {
				if n, err := w.Write(c); err != nil || n != len(c) {
					t.Fatalf("write n=%d err=%v", n, err)
				}
			}
			tail := w.tailString()
			if len(tail) != tailMax {
				t.Fatalf("tail len %d, want %d", len(tail), tailMax)
			}
			if tail != string(big[len(big)-tailMax:]) || !strings.HasSuffix(tail, "Z") {
				t.Fatalf("tail does not end with the last bytes written")
			}
			head, seen, truncated := w.result()
			if head != string(big[:4<<10]) || seen != len(big) || !truncated {
				t.Fatalf("head changed: len=%d seen=%d truncated=%v", len(head), seen, truncated)
			}
			if reported.String() != head {
				t.Fatal("observer saw bytes past the head budget")
			}
			if cap(w.tail) > 2*tailMax {
				t.Fatalf("tail buffer grew to %d", cap(w.tail))
			}
		})
	}
}

func TestProcessStreamWriterNoTailByDefault(t *testing.T) {
	w := newProcessStreamWriter(8, "id", ProcessStreamStdout, nil)
	_, _ = w.Write([]byte("0123456789"))
	if got := w.tailString(); got != "" {
		t.Fatalf("tail without TailMax = %q", got)
	}
}

func TestRunProcessTailMax(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:      []string{"bash", "-c", "for i in $(seq 1 5000); do echo line$i; done; echo LAST"},
		MaxOutput: 64,
		TailMax:   32,
		Combine:   true,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Tail, "LAST\n") || len(res.Tail) != 32 {
		t.Fatalf("tail = %q", res.Tail)
	}
	if !strings.HasPrefix(res.Output, "line1\n") || len(res.Output) != 64 {
		t.Fatalf("head = %q", res.Output)
	}

	short, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "echo hi"}, TailMax: 1024, Combine: true,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if short.Tail != "hi\n" {
		t.Fatalf("short tail = %q", short.Tail)
	}
}

func TestPoolsForCommandMatchesBash(t *testing.T) {
	eff, err := scheduler.Compile(nil, []scheduler.CommandRule{{Pattern: "make test*", Class: scheduler.ClassTest}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		policy  *scheduler.Effective
		command string
	}{
		{nil, "make test"},
		{eff, "make test"},
		{eff, "echo hi"},
	}
	for _, tc := range cases {
		got := PoolsForCommand(tc.policy, tc.command)
		want := bashPoolsForCommand(&Context{SchedulerPolicy: tc.policy}, tc.command)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%q: got %v want %v", tc.command, got, want)
		}
	}
	if got := PoolsForCommand(eff, "make test"); len(got) != 2 || got[1] != scheduler.PoolTest {
		t.Fatalf("make test pools = %v", got)
	}
}

func TestBashEnvExportedSecretRefs(t *testing.T) {
	t.Setenv("STRIKE_TEST_SCRIPT_TOKEN", "tok-value")
	t.Setenv("STRIKE_SECRET_SHOULD_NOT_LEAK", "leak")
	var uses []string
	env, err := BashEnv(map[string]string{"STRIKE_TEST_SCRIPT_TOKEN": "secret://env/STRIKE_TEST_SCRIPT_TOKEN"}, func(class, hash, action string) {
		uses = append(uses, class+"/"+hash+"/"+action)
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "STRIKE_TEST_SCRIPT_TOKEN=tok-value") {
		t.Fatalf("secret ref not injected: %v", env)
	}
	if strings.Contains(joined, "STRIKE_SECRET_SHOULD_NOT_LEAK") {
		t.Fatal("host env leaked")
	}
	if len(uses) != 1 || strings.Contains(uses[0], "tok-value") || !strings.HasPrefix(uses[0], "env/") {
		t.Fatalf("uses = %v", uses)
	}
	if _, err := BashEnv(map[string]string{"X": "not-a-ref"}, nil); err == nil {
		t.Fatal("bad ref accepted")
	}
}

func splitEvery(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		k := min(n, len(b))
		out = append(out, b[:k])
		b = b[k:]
	}
	return out
}
