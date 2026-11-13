package tracker

import "testing"

func TestKey(t *testing.T) {
	for _, input := range []string{"task-42", " https://st.yandex-team.ru/TASK-42?view=full#comment "} {
		key, err := Key(input)
		if err != nil || key != "TASK-42" {
			t.Fatalf("%q: %q %v", input, key, err)
		}
	}
	for _, input := range []string{"https://example.com/TASK-42", "https://user@st.yandex-team.ru/TASK-42", "TASK-0", "https://st.yandex-team.ru/a/TASK-42"} {
		if _, err := Key(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
func TestDecode(t *testing.T) {
	raw := []byte(`{"result":{"structuredContent":{"key":"TASK-42","summary":"Title"}}}`)
	issue, err := decode(raw, "TASK-42")
	if err != nil || issue.Summary != "Title" {
		t.Fatalf("%+v %v", issue, err)
	}
	if _, err = decode(raw, "TASK-43"); err == nil {
		t.Fatal("wrong ticket accepted")
	}
	if _, err = decode([]byte(`{"result":{"isError":true}}`), "TASK-42"); err == nil {
		t.Fatal("tool error accepted")
	}
}
