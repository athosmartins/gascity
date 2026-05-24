package coordstore_test

import "testing"

func TestFullMatrixAdaptersSelectsTargetBackendsInStableOrder(t *testing.T) {
	adapters := []adapterFactory{
		{name: "sqlite"},
		{name: "badger"},
		{name: "hqstore"},
		{name: "authorcore"},
		{name: "sqlite-cgo"},
		{name: "bbolt"},
	}

	got := fullMatrixAdapters(adapters)
	var names []string
	for _, adapter := range got {
		names = append(names, adapter.name)
	}
	want := []string{"hqstore", "bbolt", "sqlite-cgo", "badger"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}
