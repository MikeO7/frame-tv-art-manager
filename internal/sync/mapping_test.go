//nolint:goconst
package sync

import (
	"reflect"
	"testing"
)

const (
	valID1   = "id1"
	testJPG  = "test.jpg"
	testAJPG = "a.jpg"
	testBJPG = "b.jpg"
)

func TestMapping_Set(t *testing.T) {
	m := &Mapping{
		data: make(map[string]string),
	}

	m.Set(testJPG, valID1)
	if m.data[testJPG] != valID1 {
		t.Errorf("expected id1, got %s", m.data[testJPG])
	}

	m.Set(testJPG, "id2")
	if m.data[testJPG] != "id2" {
		t.Errorf("expected id2, got %s", m.data[testJPG])
	}
}

func TestMapping_Delete(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	m.Delete(testJPG)
	if _, ok := m.data[testJPG]; ok {
		t.Error("expected test.jpg to be deleted")
	}

	// Deleting non-existent should not panic
	m.Delete("nonexistent.jpg")
}

func TestMapping_GetContentID(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	id, ok := m.GetContentID(testJPG)
	if !ok || id != valID1 {
		t.Errorf("expected (id1, true), got (%s, %v)", id, ok)
	}

	id, ok = m.GetContentID("missing.jpg")
	if ok || id != "" {
		t.Errorf("expected (empty, false), got (%s, %v)", id, ok)
	}
}

func TestMapping_GetFilename(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	file, ok := m.GetFilename(valID1)
	if !ok || file != testJPG {
		t.Errorf("expected (test.jpg, true), got (%s, %v)", file, ok)
	}

	file, ok = m.GetFilename("missing_id")
	if ok || file != "" {
		t.Errorf("expected (empty, false), got (%s, %v)", file, ok)
	}
}

func TestMapping_AllContentIDs(t *testing.T) {
	initial := map[string]string{
		testAJPG: "id-a",
		testBJPG: "id-b",
	}
	m := &Mapping{
		data: map[string]string{
			testAJPG: "id-a",
			testBJPG: "id-b",
		},
	}

	got := m.AllContentIDs()
	if !reflect.DeepEqual(got, initial) {
		t.Errorf("expected %v, got %v", initial, got)
	}

	// Modifying copy should not affect original
	got["c.jpg"] = "id-c"
	if _, ok := m.data["c.jpg"]; ok {
		t.Error("modifying copy affected internal state")
	}
}

func TestMapping_TrackedFilenames(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testAJPG: "id-a",
			testBJPG: "id-b",
		},
	}
	expected := map[string]struct{}{
		testAJPG: {},
		testBJPG: {},
	}

	got := m.TrackedFilenames()
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("expected %v, got %v", expected, got)
	}
}
