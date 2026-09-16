package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseAliasesRaw(t *testing.T) {
	out := `# comment
support@example.com admin@example.com
sales@example.com admin@example.com, jane@example.com

@example.com postmaster@example.com
`
	want := []alias{
		{address: "support@example.com", recipients: []string{"admin@example.com"}},
		{address: "sales@example.com", recipients: []string{"admin@example.com", "jane@example.com"}},
		{address: "@example.com", recipients: []string{"postmaster@example.com"}},
	}
	got := parseAliases(out)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseAliases() = %#v, want %#v", got, want)
	}
}

func TestParseAliasesSetupFormat(t *testing.T) {
	out := `* support@example.com admin@example.com

* sales@example.com admin@example.com
`
	want := []alias{
		{address: "support@example.com", recipients: []string{"admin@example.com"}},
		{address: "sales@example.com", recipients: []string{"admin@example.com"}},
	}
	got := parseAliases(out)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseAliases() = %#v, want %#v", got, want)
	}
}

func TestParseAliasesEmpty(t *testing.T) {
	for _, out := range []string{"", "# only comments\n", "\n\n"} {
		if got := parseAliases(out); len(got) != 0 {
			t.Errorf("parseAliases(%q) = %#v, want no aliases", out, got)
		}
	}
}

func TestFilterAliases(t *testing.T) {
	aliases := []alias{
		{address: "a@example.com", recipients: []string{"admin@example.com"}},
		{address: "b@example.com", recipients: []string{"jane@example.com", "admin@example.com"}},
		{address: "c@example.com", recipients: []string{"jane@example.com"}},
	}
	got := filterAliases(aliases, "admin")
	want := []alias{aliases[0], aliases[1]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterAliases() = %#v, want %#v", got, want)
	}
}

func TestRenderAliasPage(t *testing.T) {
	var aliases []alias
	for i := 0; i < 25; i++ {
		aliases = append(aliases, alias{address: fmt.Sprintf("alias%02d@example.com", i), recipients: []string{"admin@example.com"}})
	}

	text, nav, shown, total := renderAliasPage(aliases, 0, "")
	if total != 3 {
		t.Errorf("total pages = %d, want 3", total)
	}
	if shown != 0 {
		t.Errorf("shown page = %d, want 0", shown)
	}
	if !strings.Contains(text, "25 alias(es) · page 1/3:") {
		t.Errorf("unexpected header: %q", text)
	}
	if got := strings.Count(text, "•"); got != 10 {
		t.Errorf("page 0 lists %d aliases, want 10", got)
	}
	if len(nav) != 2 {
		t.Errorf("page 0 nav has %d buttons, want 2 (indicator + next)", len(nav))
	}

	// Out-of-range pages are clamped to the last page.
	_, _, shown, _ = renderAliasPage(aliases, 99, "")
	if shown != 2 {
		t.Errorf("clamped page = %d, want 2", shown)
	}

	_, nav, _, _ = renderAliasPage(aliases, 1, "")
	if len(nav) != 3 {
		t.Errorf("page 1 nav has %d buttons, want 3 (prev + indicator + next)", len(nav))
	}

	text, nav, _, _ = renderAliasPage(aliases, 2, "")
	if got := strings.Count(text, "•"); got != 5 {
		t.Errorf("last page lists %d aliases, want 5", got)
	}
	if len(nav) != 2 {
		t.Errorf("last page nav has %d buttons, want 2 (prev + indicator)", len(nav))
	}
}

func TestRenderAliasPageEmpty(t *testing.T) {
	text, nav, shown, total := renderAliasPage(nil, 0, "")
	if text != "No aliases configured." {
		t.Errorf("text = %q, want %q", text, "No aliases configured.")
	}
	if nav != nil {
		t.Errorf("nav = %#v, want nil", nav)
	}
	if shown != 0 || total != 1 {
		t.Errorf("shown/total = %d/%d, want 0/1", shown, total)
	}
}

func TestRenderAliasPageFilter(t *testing.T) {
	aliases := []alias{
		{address: "a@example.com", recipients: []string{"admin@example.com"}},
		{address: "b@example.com", recipients: []string{"jane@example.com"}},
	}
	text, _, _, _ := renderAliasPage(aliases, 0, "admin")
	if !strings.Contains(text, "2 alias(es) for <b>admin</b> · page 1/1:") {
		t.Errorf("unexpected filtered header: %q", text)
	}
}

func TestNormalizeLocalPart(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "support", want: "support", ok: true},
		{in: "Support@EXAMPLE.com", want: "support", ok: true},
		{in: "first.name", want: "first.name", ok: true},
		{in: "-bad", ok: false},
		{in: "bad space", ok: false},
		{in: "bad@bad@bad", ok: false},
	}
	for _, tt := range tests {
		got, err := normalizeLocalPart(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("normalizeLocalPart(%q) error = %v, want ok=%v", tt.in, err, tt.ok)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("normalizeLocalPart(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		n, lo, hi, want int
	}{
		{n: 0, lo: 0, hi: 2, want: 0},
		{n: -1, lo: 0, hi: 2, want: 0},
		{n: 5, lo: 0, hi: 2, want: 2},
		{n: 2, lo: 0, hi: 2, want: 2},
	}
	for _, tt := range tests {
		if got := clamp(tt.n, tt.lo, tt.hi); got != tt.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tt.n, tt.lo, tt.hi, got, tt.want)
		}
	}
}

// fakeDockerExec replaces dockerExec for the duration of a test. It returns
// canned output based on which database the command touches, so tests can
// simulate the account and alias files independently.
func fakeDockerExec(t *testing.T, accounts, aliases string, accountsMissing bool) {
	t.Helper()
	saved := dockerExec
	t.Cleanup(func() { dockerExec = saved })
	dockerExec = func(args ...string) (string, error) {
		cmd := strings.Join(args, " ")
		switch {
		case strings.Contains(cmd, accountsFile):
			if accountsMissing {
				return "", fmt.Errorf("exit status 1")
			}
			return accounts, nil
		case strings.Contains(cmd, aliasFile):
			return aliases, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", cmd)
		}
	}
}

func TestReadAccounts(t *testing.T) {
	fakeDockerExec(t, "admin@example.com|{SHA512-CRYPT}abc\n# comment\nJane@Example.com|xyz\n", "", false)
	got, err := readAccounts()
	if err != nil {
		t.Fatalf("readAccounts() error = %v", err)
	}
	want := map[string]bool{"admin@example.com": true, "jane@example.com": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readAccounts() = %#v, want %#v", got, want)
	}
}

func TestReadAccountsMissingFile(t *testing.T) {
	fakeDockerExec(t, "", "", true)
	if _, err := readAccounts(); err == nil {
		t.Error("readAccounts() error = nil, want error for missing database")
	}
}

func TestMailboxExists(t *testing.T) {
	tests := []struct {
		name     string
		accounts string
		aliases  string
		addr     string
		want     bool
	}{
		{
			name:     "account",
			accounts: "admin@example.com|hash\n",
			addr:     "admin@example.com",
			want:     true,
		},
		{
			name:    "alias chain",
			aliases: "sales@example.com admin@example.com\n",
			addr:    "sales@example.com",
			want:    true,
		},
		{
			name:    "case insensitive",
			aliases: "Sales@Example.com admin@example.com\n",
			addr:    "sales@example.com",
			want:    true,
		},
		{
			name:     "neither",
			accounts: "admin@example.com|hash\n",
			aliases:  "sales@example.com admin@example.com\n",
			addr:     "ghost@example.com",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDockerExec(t, tt.accounts, tt.aliases, false)
			got, err := mailboxExists(tt.addr)
			if err != nil {
				t.Fatalf("mailboxExists(%q) error = %v", tt.addr, err)
			}
			if got != tt.want {
				t.Errorf("mailboxExists(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestMailboxExistsMissingDatabase(t *testing.T) {
	fakeDockerExec(t, "", "", true)
	if _, err := mailboxExists("admin@example.com"); err == nil {
		t.Error("mailboxExists() error = nil, want error for missing database")
	}
}
